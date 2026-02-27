package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	rie "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/aws-lambda-rie"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/model"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/supervisor/local"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/utils"
	log "github.com/sirupsen/logrus"
)

func runManaged(lsOpts *LsOpts) {
	configureManagedLogger(lsOpts.InitLogLevel)

	// Initialize X-Ray daemon
	d := doInitDaemon(
		lsOpts.LocalstackIP,
		lsOpts.EdgePort,
		lsOpts.EnableXRayTelemetry == "1",
		lsOpts.InitLogLevel,
	)

	defer func() {
		log.Debugln("Shutting down xray daemon")
		d.stop()
		log.Debugln("Flushing segments in xray daemon")
		d.close()
	}()

	var credential *syscall.Credential
	if IsRootUser() && lsOpts.User != "" && lsOpts.User != "root" {
		uid := 993
		gid := 990
		AddUser(lsOpts.User, uid, gid)
		if err := os.Chown("/tmp", uid, gid); err != nil {
			log.Warnln("Could not change owner of directory /tmp:", err)
		}

		credential = &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		}

		UserLogger().Debugln("Configured runtime to run as non-root user:", lsOpts.User)
	}

	adapter := LocalStackAdapter{
		UpstreamEndpoint: lsOpts.RuntimeEndpoint,
		RuntimeId:        lsOpts.RuntimeId,
	}

	rieAddr := fmt.Sprintf("0.0.0.0:%s", lsOpts.InteropPort)
	rapiAddr := "127.0.0.1:9001"

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	logCollector := NewLogCollector()

	var supervOpts []local.ProcessSupervisorOption
	supervOpts = append(supervOpts, local.WithLowerPriorities(false))
	if credential != nil {
		supervOpts = append(supervOpts, local.WithProcessCredential(credential))
	}
	supv := local.NewProcessSupervisor(supervOpts...)

	fileUtil := utils.NewFileUtil()

	invokeTimeoutEnv := GetEnvOrDie("AWS_LAMBDA_FUNCTION_TIMEOUT")
	invokeTimeoutSeconds, err := strconv.Atoi(invokeTimeoutEnv)
	if err != nil {
		log.Fatalln(err)
	}

	raptorApp, err := rie.Run(
		rapiAddr, supv, fileUtil, logCollector,
	)
	if err != nil {
		log.Errorf("failed with error: %s", err.Error())
		return
	}

	initReq, err := rie.GetInitRequestMessage(fileUtil, os.Args)
	if err != nil {
		log.Errorf("could not build initialization parameters: %s", err.Error())
		return
	}

	// HACK(gregfurman): expects the account to be set via the AWS_ACCOUNT_ID env var which is undocumented
	initReq.AccountID = lsOpts.AccountId
	initReq.FunctionARN = fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s:%s", initReq.AwsRegion, initReq.AccountID, initReq.TaskName, initReq.FunctionVersion)
	// Convert seconds to time.Duration (invokeTimeoutSeconds is in seconds, need to convert to Duration)
	initReq.InvokeTimeout = model.DurationMS(time.Duration(invokeTimeoutSeconds) * time.Second)

	runtimeAPIAddr := raptorApp.RuntimeAPIAddrPort()

	rieHandler, err := NewInvokeHandler(
		*lsOpts, initReq, raptorApp, logCollector,
	)
	if err != nil {
		log.Fatal("creating RIE handler error:", err)
	}

	if err := rieHandler.Init(); err != nil {
		log.Warn("INIT failed", "err", err)
	}

	server, err := rie.StartServer(raptorApp, rieHandler, rieAddr, sigChan)
	if err != nil {
		log.Fatal("Proxy ListenAndServe error:", err)
	}

	// go RunHotReloadingListener(interopServer, lsOpts.HotReloadingPaths, fileWatcherContext, lsOpts.FileWatcherStrategy)

	log.Debugf("Awaiting initialization of runtime api at %s.", runtimeAPIAddr.String())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := waitForRuntimeAPI(ctx, runtimeAPIAddr.String()); err != nil {
		log.Fatalf("Lambda Runtime API server at %s did not come up in 30s, with error %s", runtimeAPIAddr.String(), err.Error())
	}
	cancel()

	log.Debugln("Completed initialization of runtime. Sending status ready to LocalStack.")
	if err := adapter.SendStatus(Ready, []byte{}); err != nil {
		log.Fatalln("Failed to send status ready to LocalStack", err, ". Exiting.")
	}

	select {
	case <-server.Done():
		if err := server.Err(); err != nil {
			log.Warn("rie server stopped", "err", err)
			os.Exit(1)
		}
	case <-raptorApp.Done():
		if err := raptorApp.Err(); err != nil {
			log.Errorln("Runtime error:", err)
		}
	}

}
