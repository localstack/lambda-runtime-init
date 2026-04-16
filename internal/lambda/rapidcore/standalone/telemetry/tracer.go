// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/appctx"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/metering"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/rapi/model"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/telemetry"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	awsxray "github.com/aws/aws-sdk-go/service/xray"
	"github.com/sirupsen/logrus"
)

// InitSubsegmentName provides name attribute for Init subsegment
const InitSubsegmentName = "Initialization"

// RestoreSubsegmentName provides name attribute for Restore subsegment
const RestoreSubsegmentName = "Restore"

// InvokeSubsegmentName provides name attribute for Invoke subsegment
const InvokeSubsegmentName = "Invocation"

// OverheadSubsegmentName provides name attribute for Overhead subsegment
const OverheadSubsegmentName = "Overhead"

type TracingEvent struct {
	Message     string `json:"message"`
	TraceID     string `json:"trace_id"`
	SegmentName string `json:"segment_name"`
	SegmentID   string `json:"segment_id"`
	Timestamp   int64  `json:"timestamp"`
}

type xraySubsegmentDoc struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
}

type xraySegmentDoc struct {
	Name        string              `json:"name"`
	ID          string              `json:"id"`
	TraceID     string              `json:"trace_id"`
	StartTime   float64             `json:"start_time"`
	EndTime     float64             `json:"end_time"`
	ParentID    string              `json:"parent_id,omitempty"`
	Type        string              `json:"type,omitempty"`   // "subsegment" when parent present
	Origin      string              `json:"origin,omitempty"` // "AWS::Lambda::Function"
	Subsegments []xraySubsegmentDoc `json:"subsegments,omitempty"`
}

type StandaloneTracer struct {
	startFunction          func(ctx context.Context, invoke *interop.Invoke, segmentName string, timestamp int64)
	endFunction            func(ctx context.Context, invoke *interop.Invoke, segmentName string, timestamp int64)
	invoke                 *interop.Invoke
	tracingHeader          string
	rootTraceID            string
	parent                 string
	sampled                string
	lineage                string
	invocationSubsegmentID string
	initStartTime          int64
	initEndTime            int64
	restoreStartTime       int64
	restoreEndTime         int64
	restorePresent         bool

	xrayClient         *awsxray.XRay
	mu                 sync.Mutex
	segmentStartTimes  map[string]int64    // segmentName → start nanoseconds
	segmentIDs         map[string]string   // segmentName → 16-char hex ID
	pendingSubsegments []xraySubsegmentDoc // accumulated per-invocation
}

func generateSegmentID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (t *StandaloneTracer) Configure(invoke *interop.Invoke) {
	t.invoke = invoke
	t.tracingHeader = invoke.TraceID
	t.invocationSubsegmentID = ""
	t.rootTraceID, t.parent, t.sampled, t.lineage = telemetry.ParseTracingHeader(invoke.TraceID)
	if invoke.RestoreDurationNs == 0 {
		t.restorePresent = false
	} else {
		t.restorePresent = true
		t.restoreStartTime = metering.MonoToEpoch(invoke.RestoreStartTimeMonotime)
		t.restoreEndTime = t.restoreStartTime + invoke.RestoreDurationNs
	}
	t.mu.Lock()
	t.pendingSubsegments = nil
	t.segmentStartTimes = make(map[string]int64)
	t.segmentIDs = make(map[string]string)
	t.mu.Unlock()
}

func (t *StandaloneTracer) CaptureInvokeSegment(ctx context.Context, criticalFunction func(context.Context) error) error {
	return t.withStartAndEnd(ctx, criticalFunction, "STANDALONE_FUNCTION_NAME")
}

func (t *StandaloneTracer) CaptureInitSubsegment(ctx context.Context, criticalFunction func(context.Context) error) error {
	return t.withStartAndEnd(ctx, criticalFunction, InitSubsegmentName)
}

func (t *StandaloneTracer) CaptureInvokeSubsegment(ctx context.Context, criticalFunction func(context.Context) error) error {
	err := t.withStartAndEnd(ctx, criticalFunction, InvokeSubsegmentName)
	t.mu.Lock()
	t.invocationSubsegmentID = t.segmentIDs[InvokeSubsegmentName]
	t.mu.Unlock()
	return err
}

func (t *StandaloneTracer) CaptureOverheadSubsegment(ctx context.Context, criticalFunction func(context.Context) error) error {
	return t.withStartAndEnd(ctx, criticalFunction, OverheadSubsegmentName)
}

func (t *StandaloneTracer) withStartAndEnd(ctx context.Context, criticalFunction func(context.Context) error, segmentName string) error {
	segID := generateSegmentID()
	t.mu.Lock()
	t.segmentIDs[segmentName] = segID
	t.mu.Unlock()
	ctx = telemetry.NewTraceContext(ctx, t.rootTraceID, segID)
	t.startFunction(ctx, t.invoke, segmentName, time.Now().UnixNano())
	err := criticalFunction(ctx)
	t.endFunction(ctx, t.invoke, segmentName, time.Now().UnixNano())
	return err
}

func (t *StandaloneTracer) RecordInitStartTime() {
	t.initStartTime = time.Now().UnixNano()
}

func (t *StandaloneTracer) RecordInitEndTime() {
	t.initEndTime = time.Now().UnixNano()
}

func (t *StandaloneTracer) sendPrepSubsegment(ctx context.Context, subsegmentName string, startTime int64, endTime int64) {
	segID := generateSegmentID()
	t.mu.Lock()
	t.segmentIDs[subsegmentName] = segID
	t.mu.Unlock()
	ctx = telemetry.NewTraceContext(ctx, t.rootTraceID, segID)
	t.startFunction(ctx, t.invoke, subsegmentName, startTime)
	t.endFunction(ctx, t.invoke, subsegmentName, endTime)
}

func (t *StandaloneTracer) SendInitSubsegmentWithRecordedTimesOnce(ctx context.Context) {
	t.sendPrepSubsegment(ctx, InitSubsegmentName, t.initStartTime, t.initEndTime)
}

func (t *StandaloneTracer) SendRestoreSubsegmentWithRecordedTimesOnce(ctx context.Context) {
	if t.restorePresent {
		t.sendPrepSubsegment(ctx, RestoreSubsegmentName, t.restoreStartTime, t.restoreEndTime)
	}
}

func (t *StandaloneTracer) MarkError(ctx context.Context)                                    {}
func (t *StandaloneTracer) AttachErrorCause(ctx context.Context, errorCause json.RawMessage) {}

func (t *StandaloneTracer) WithErrorCause(ctx context.Context, appCtx appctx.ApplicationContext, criticalFunction func(ctx context.Context) error) func(ctx context.Context) error {
	return criticalFunction
}
func (t *StandaloneTracer) WithError(ctx context.Context, appCtx appctx.ApplicationContext, criticalFunction func(ctx context.Context) error) func(ctx context.Context) error {
	return criticalFunction
}

func (t *StandaloneTracer) BuildTracingHeader() func(ctx context.Context) string {
	return func(ctx context.Context) string {
		var parent string
		var ok bool

		if parent, ok = ctx.Value(telemetry.DocumentIDKey).(string); !ok || parent == "" {
			return t.invoke.TraceID
		}

		if t.rootTraceID == "" || t.sampled == "" {
			return ""
		}

		var tracingHeader = "Root=%s;Parent=%s;Sampled=%s"

		if t.lineage == "" {
			return fmt.Sprintf(tracingHeader, t.rootTraceID, parent, t.sampled)
		}

		return fmt.Sprintf(tracingHeader+";Lineage=%s", t.rootTraceID, parent, t.sampled, t.lineage)
	}
}

func (t *StandaloneTracer) BuildTracingCtxForStart() *interop.TracingCtx {
	if t.rootTraceID == "" || t.sampled != model.XRaySampled {
		return nil
	}

	return &interop.TracingCtx{
		SpanID: t.parent,
		Type:   model.XRayTracingType,
		Value:  telemetry.BuildFullTraceID(t.rootTraceID, t.invoke.LambdaSegmentID, t.sampled),
	}
}

func (t *StandaloneTracer) BuildTracingCtxAfterInvokeComplete() *interop.TracingCtx {
	if t.rootTraceID == "" || t.sampled != model.XRaySampled || t.invocationSubsegmentID == "" {
		return nil
	}

	return &interop.TracingCtx{
		SpanID: t.invocationSubsegmentID,
		Type:   model.XRayTracingType,
		Value:  t.tracingHeader,
	}
}

func (t *StandaloneTracer) sendToXRay(seg xraySegmentDoc) {
	if t.xrayClient == nil {
		return
	}
	data, err := json.Marshal(seg)
	if err != nil {
		log.WithError(err).Error("xray: failed to marshal segment")
		return
	}
	doc := aws.String(string(data))
	if _, err := t.xrayClient.PutTraceSegments(&awsxray.PutTraceSegmentsInput{
		TraceSegmentDocuments: []*string{doc},
	}); err != nil {
		log.WithError(err).Warn("xray: PutTraceSegments failed")
	}
}

func isTracingEnabled(root, parent, sampled string) bool {
	return len(root) != 0 && len(parent) != 0 && sampled == "1"
}

func NewStandaloneTracer() *StandaloneTracer {
	tracer := &StandaloneTracer{
		segmentStartTimes:  make(map[string]int64),
		segmentIDs:         make(map[string]string),
		pendingSubsegments: nil,
	}

	// Use the X-Ray daemon's HTTP proxy as the SDK endpoint.
	// AWS_XRAY_DAEMON_ADDRESS is the standard Lambda env var (default 127.0.0.1:2000).
	daemonAddr := os.Getenv("AWS_XRAY_DAEMON_ADDRESS")
	if daemonAddr == "" {
		daemonAddr = "127.0.0.1:2000"
	}
	endpoint := "http://" + daemonAddr

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}

	awsCfg := &aws.Config{
		Endpoint: aws.String(endpoint),
	}
	if region != "" {
		awsCfg.Region = aws.String(region)
	}
	if sess, err := session.NewSession(awsCfg); err == nil {
		tracer.xrayClient = awsxray.New(sess)
	} else {
		log.WithError(err).Warn("xray: failed to initialize client, traces will not be sent")
	}

	startCaptureFn := func(ctx context.Context, i *interop.Invoke, segmentName string, timestamp int64) {
		root, parent, sampled, _ := telemetry.ParseTracingHeader(i.TraceID)
		if !isTracingEnabled(root, parent, sampled) {
			return
		}
		tracer.mu.Lock()
		tracer.segmentStartTimes[segmentName] = timestamp
		tracer.mu.Unlock()
	}

	endCaptureFn := func(ctx context.Context, i *interop.Invoke, segmentName string, timestamp int64) {
		root, parent, sampled, _ := telemetry.ParseTracingHeader(i.TraceID)
		if !isTracingEnabled(root, parent, sampled) {
			return
		}

		tracer.mu.Lock()
		startTime := tracer.segmentStartTimes[segmentName]
		segID := tracer.segmentIDs[segmentName]
		tracer.mu.Unlock()

		if segmentName == "STANDALONE_FUNCTION_NAME" {
			// Prefer the LambdaSegmentID assigned by the invoker if present.
			rootSegID := i.LambdaSegmentID
			if rootSegID == "" {
				rootSegID = segID
			}
			functionName := os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
			if functionName == "" {
				functionName = "function"
			}
			seg := xraySegmentDoc{
				Name:        functionName,
				ID:          rootSegID,
				TraceID:     root,
				StartTime:   float64(startTime) / 1e9,
				EndTime:     float64(timestamp) / 1e9,
				Origin:      "AWS::Lambda::Function",
				Subsegments: tracer.pendingSubsegments,
			}
			// If there is an upstream parent (e.g. API Gateway → Lambda), reference it.
			if parent != "" && parent != rootSegID {
				seg.ParentID = parent
				seg.Type = "subsegment"
			}
			tracer.sendToXRay(seg)
		} else {
			sub := xraySubsegmentDoc{
				ID:        segID,
				Name:      segmentName,
				StartTime: float64(startTime) / 1e9,
				EndTime:   float64(timestamp) / 1e9,
			}
			tracer.mu.Lock()
			tracer.pendingSubsegments = append(tracer.pendingSubsegments, sub)
			tracer.mu.Unlock()
			log.WithFields(logrus.Fields{
				"segment": segmentName,
				"id":      segID,
			}).Debug("xray: subsegment recorded")
		}
	}

	tracer.startFunction = startCaptureFn
	tracer.endFunction = endCaptureFn
	return tracer
}
