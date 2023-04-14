// The implementation is based on the open source AWS xray daemon https://github.com/aws/aws-xray-daemon (cmd/tracing/daemon.go)
// It has been adapted for the use as a library and not as a separate executable.
// The config is set directly in code instead of loading it from a config file

package main

import (
	"encoding/json"
	"io"
	"math"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/aws/aws-xray-daemon/pkg/bufferpool"
	"github.com/aws/aws-xray-daemon/pkg/cfg"
	"github.com/aws/aws-xray-daemon/pkg/conn"
	"github.com/aws/aws-xray-daemon/pkg/logger"
	"github.com/aws/aws-xray-daemon/pkg/processor"
	"github.com/aws/aws-xray-daemon/pkg/proxy"
	"github.com/aws/aws-xray-daemon/pkg/ringbuffer"
	"github.com/aws/aws-xray-daemon/pkg/socketconn"
	"github.com/aws/aws-xray-daemon/pkg/socketconn/udp"
	"github.com/aws/aws-xray-daemon/pkg/telemetry"
	"github.com/aws/aws-xray-daemon/pkg/tracesegment"
	"github.com/aws/aws-xray-daemon/pkg/util"

	"github.com/aws/aws-sdk-go/aws"
	log "github.com/cihub/seelog"
	"github.com/shirou/gopsutil/mem"
)

const protocolSeparator = "\n"

// Log Rotation Size is 50 MB
const logRotationSize int64 = 50 * 1024 * 1024

var noMetadata = true

var logFile string
var logLevel string

// Daemon reads trace segments from X-Ray daemon address and
// send to X-Ray service.
type Daemon struct {
	receiverCount     int
	processorCount    int
	receiveBufferSize int
	enableTelemetry   bool

	// Boolean channel, set to true if error is received reading from Socket.
	done chan bool

	// Ring buffer, used to stored segments received.
	std *ringbuffer.RingBuffer

	// Counter for segments read by daemon.
	count uint64

	// Instance of socket connection.
	sock socketconn.SocketConn

	// Reference to buffer pool.
	pool *bufferpool.BufferPool

	// Reference to Processor.
	processor *processor.Processor

	// HTTP Proxy server
	server *proxy.Server
}

func initConfig(endpoint string) *cfg.Config {
	xrayConfig := cfg.DefaultConfig()
	xrayConfig.Socket.UDPAddress = "127.0.0.1:2000"
	xrayConfig.Socket.TCPAddress = "127.0.0.1:2000"
	xrayConfig.Endpoint = endpoint
	xrayConfig.NoVerifySSL = util.Bool(true) // obvious
	xrayConfig.LocalMode = util.Bool(true)   // skip EC2 metadata check
	xrayConfig.Region = GetEnvOrDie("AWS_REGION")
	xrayConfig.Logging.LogLevel = "info"
	//xrayConfig.TotalBufferSizeMB
	//xrayConfig.RoleARN = roleARN

	return xrayConfig
}

func initDaemon(config *cfg.Config, enableTelemetry bool) *Daemon {
	if logFile != "" {
		var fileWriter io.Writer
		if *config.Logging.LogRotation {
			// Empty Archive path as code does not archive logs
			apath := ""
			maxSize := logRotationSize
			// Keep one rolled over log file around
			maxRolls := 1
			archiveExplode := false
			fileWriter, _ = log.NewRollingFileWriterSize(logFile, 0, apath, maxSize, maxRolls, 0, archiveExplode)
		} else {
			fileWriter, _ = log.NewFileWriter(logFile)
		}
		logger.LoadLogConfig(fileWriter, config, logLevel)
	} else {
		newWriter, _ := log.NewConsoleWriter()
		logger.LoadLogConfig(newWriter, config, logLevel)
	}
	defer log.Flush()

	log.Infof("Initializing AWS X-Ray daemon %v", cfg.Version)

	parameterConfig := cfg.ParameterConfigValue
	parameterConfig.Processor.BatchSize = 10
	parameterConfig.Processor.IdleTimeoutMillisecond = 1000
	receiveBufferSize := parameterConfig.Socket.BufferSizeKB * 1024

	var sock socketconn.SocketConn

	sock = udp.New(config.Socket.UDPAddress)

	memoryLimit := evaluateBufferMemory(0)
	log.Infof("Using buffer memory limit of %v MB", memoryLimit)
	buffers, err := bufferpool.GetPoolBufferCount(memoryLimit, receiveBufferSize)
	if err != nil {
		log.Errorf("%v", err)
		os.Exit(1)
	}
	log.Infof("%v segment buffers allocated", buffers)
	bufferPool := bufferpool.Init(buffers, receiveBufferSize)
	std := ringbuffer.New(buffers, bufferPool)
	if config.Endpoint != "" {
		log.Debugf("Using Endpoint read from Config file: %s", config.Endpoint)
	}
	awsConfig, session := conn.GetAWSConfigSession(&conn.Conn{}, config, config.RoleARN, config.Region, noMetadata)
	log.Infof("Using region: %v", aws.StringValue(awsConfig.Region))

	if enableTelemetry {
		// Telemetry can be quite verbose, for example 10+ PutTelemetryRecords requests for a single invocation
		telemetry.Init(awsConfig, session, config.ResourceARN, noMetadata)
	} else {
		// Telemetry cannot be nil because it is used internally in the X-Ray daemon, for example in batchprocessor.go
		// We assume that SegmentReceived is never invoked internally in X-Ray because it enables postTelemetry.
		telemetry.T = telemetry.GetTestTelemetry()
	}

	// If calculated number of buffer is lower than our default, use calculated one. Otherwise, use default value.
	parameterConfig.Processor.BatchSize = util.GetMinIntValue(parameterConfig.Processor.BatchSize, buffers)

	// Create proxy http server
	server, err := proxy.NewServer(config, awsConfig, session)
	if err != nil {
		log.Errorf("Unable to start http proxy server: %v", err)
		os.Exit(1)
	}
	processorCount := 2

	daemon := &Daemon{
		receiverCount:     parameterConfig.ReceiverRoutines,
		processorCount:    processorCount,
		receiveBufferSize: receiveBufferSize,
		enableTelemetry:   enableTelemetry,
		done:              make(chan bool),
		std:               std,
		pool:              bufferPool,
		count:             0,
		sock:              sock,
		server:            server,
		processor:         processor.New(awsConfig, session, processorCount, std, bufferPool, parameterConfig),
	}

	return daemon
}

func runDaemon(daemon *Daemon) {
	// Start http server for proxying requests to xray
	go daemon.server.Serve()

	for i := 0; i < daemon.receiverCount; i++ {
		go daemon.poll()
	}
}

func (d *Daemon) close() {
	for i := 0; i < d.receiverCount; i++ {
		<-d.done
	}
	// Signal routines to finish
	// This will push telemetry and customer segments in parallel
	d.std.Close()
	if d.enableTelemetry {
		telemetry.T.Quit <- true
	}

	<-d.processor.Done
	if d.enableTelemetry {
		<-telemetry.T.Done
	}

	log.Debugf("Trace segment: received: %d, truncated: %d, processed: %d", atomic.LoadUint64(&d.count), d.std.TruncatedCount(), d.processor.ProcessedCount())
	log.Debugf("Shutdown finished. Current epoch in nanoseconds: %v", time.Now().UnixNano())
}

func (d *Daemon) stop() {
	d.sock.Close()
	d.server.Close()
}

// Returns number of bytes read from socket connection.
func (d *Daemon) read(buf *[]byte) int {
	bufVal := *buf
	rlen, err := d.sock.Read(bufVal)
	switch err := err.(type) {
	case net.Error:
		if !err.Temporary() {
			d.done <- true
			return -1
		}
		log.Errorf("daemon: net: err: %v", err)
		return 0
	case error:
		log.Errorf("daemon: socket: err: %v", err)
		return 0
	}
	return rlen
}

func (d *Daemon) poll() {
	separator := []byte(protocolSeparator)
	fallBackBuffer := make([]byte, d.receiveBufferSize)
	splitBuf := make([][]byte, 2)

	for {
		bufPointer := d.pool.Get()
		fallbackPointerUsed := false
		if bufPointer == nil {
			log.Debug("Pool does not have any buffer.")
			bufPointer = &fallBackBuffer
			fallbackPointerUsed = true
		}
		rlen := d.read(bufPointer)
		if rlen > 0 && d.enableTelemetry {
			telemetry.T.SegmentReceived(1)
		}
		if rlen == 0 {
			if !fallbackPointerUsed {
				d.pool.Return(bufPointer)
			}
			continue
		}
		if fallbackPointerUsed {
			log.Warn("Segment dropped. Consider increasing memory limit")
			if d.enableTelemetry {
				telemetry.T.SegmentSpillover(1)
			}
			continue
		} else if rlen == -1 {
			return
		}

		buf := *bufPointer
		bufMessage := buf[0:rlen]

		slices := util.SplitHeaderBody(&bufMessage, &separator, &splitBuf)
		if len(slices[1]) == 0 {
			log.Warnf("Missing header or segment: %s", string(slices[0]))
			d.pool.Return(bufPointer)
			if d.enableTelemetry {
				telemetry.T.SegmentRejected(1)
			}
			continue
		}

		header := slices[0]
		payload := slices[1]
		headerInfo := tracesegment.Header{}
		json.Unmarshal(header, &headerInfo)

		switch headerInfo.IsValid() {
		case true:
		default:
			log.Warnf("Invalid header: %s", string(header))
			d.pool.Return(bufPointer)
			if d.enableTelemetry {
				telemetry.T.SegmentRejected(1)
			}
			continue
		}

		ts := &tracesegment.TraceSegment{
			Raw:     &payload,
			PoolBuf: bufPointer,
		}

		atomic.AddUint64(&d.count, 1)
		d.std.Send(ts)
	}
}

func evaluateBufferMemory(cliBufferMemory int) int {
	var bufferMemoryMB int
	if cliBufferMemory > 0 {
		bufferMemoryMB = cliBufferMemory
	} else {
		vm, err := mem.VirtualMemory()
		if err != nil {
			log.Errorf("%v", err)
			os.Exit(1)
		}
		bufferMemoryLimitPercentageOfTotal := 0.01
		totalBytes := vm.Total
		bufferMemoryMB = int(math.Floor(bufferMemoryLimitPercentageOfTotal * float64(totalBytes) / float64(1024*1024)))
	}
	if bufferMemoryMB < 3 {
		log.Error("Not enough Buffers Memory Allocated. Min Buffers Memory required: 3 MB.")
		os.Exit(1)
	}
	return bufferMemoryMB
}
