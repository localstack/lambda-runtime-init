package rie

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/aws-lambda-rie/internal"
	rieinvoke "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/aws-lambda-rie/internal/invoke"
	rieTelemetry "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/aws-lambda-rie/internal/telemetry"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/invoke"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/invoke/timeout"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/model"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/rapid"
	rapidmodel "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/rapid/model"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/raptor"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/servicelogs"
	supvmodel "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/supervisor/model"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/utils"
)

func NewRieInvokeRequest(r *http.Request, w http.ResponseWriter) interop.InvokeRequest {
	return rieinvoke.NewRieInvokeRequest(r, w)
}

func GetInitRequestMessage(fileUtil utils.FileUtil, args []string) (model.InitRequestMessage, rapidmodel.AppError) {
	return internal.GetInitRequestMessage(fileUtil, args)
}

func Run(
	rapiAddr string,
	supv supvmodel.ProcessSupervisor,
	fileUtil utils.FileUtil,
	logWriter io.Writer,
) (*raptor.App, error) {
	runtimeAPIAddr, err := internal.ParseAddr(rapiAddr, "127.0.0.1:9001")
	if err != nil {
		return nil, fmt.Errorf("invalid runtime API address: %w", err)
	}

	telemetryAPIRelay := rieTelemetry.NewRelay()
	eventsAPI := rieTelemetry.NewEventsAPI(telemetryAPIRelay)

	responderFactoryFunc := func(_ context.Context, invokeReq interop.InvokeRequest) invoke.InvokeResponseSender {
		return rieinvoke.NewResponder(invokeReq)
	}
	invokeRouter := invoke.NewInvokeRouter(rapid.MaxIdleRuntimesQueueSize, eventsAPI, responderFactoryFunc, timeout.NewRecentCache())

	deps := rapid.Dependencies{
		EventsAPI:                eventsAPI,
		LogsEgressAPI:            rieTelemetry.NewLogsEgress(telemetryAPIRelay, io.MultiWriter(logWriter, os.Stdout)),
		TelemetrySubscriptionAPI: rieTelemetry.NewSubscriptionAPI(telemetryAPIRelay, eventsAPI, eventsAPI),
		Supervisor:               supv,
		RuntimeAPIAddrPort:       runtimeAPIAddr,
		FileUtils:                fileUtil,
		InvokeRouter:             invokeRouter,
	}

	raptorApp, err := raptor.StartApp(deps, "", noOpLogger{})
	if err != nil {
		return nil, fmt.Errorf("could not start runtime api server: %w", err)
	}

	return raptorApp, nil
}

// noOpLogger implements the raptorLogger interface with no-op methods
type noOpLogger struct{}

func (n noOpLogger) Log(_ servicelogs.Operation, _ time.Time, _ []servicelogs.Property, _ []servicelogs.Dimension, _ []servicelogs.Metric) {
}

func (n noOpLogger) SetInitData(_ interop.InitStaticDataProvider) {}

func (n noOpLogger) Close() error { return nil }

func StartServer(
	raptorApp *raptor.App,
	rieApp http.Handler,
	rieAddr string,
	sigCh chan os.Signal,
) (*raptor.Server, error) {
	emulatorAddr, err := internal.ParseAddr(rieAddr, "0.0.0.0:8080")
	if err != nil {
		return nil, fmt.Errorf("invalid RIE address: %w", err)
	}

	s, err := raptor.StartServer(raptorApp, rieApp, &raptor.TCPAddress{AddrPort: emulatorAddr})
	if err != nil {
		return nil, fmt.Errorf("could not start RIE server: %w", err)
	}
	slog.Debug("RIE server started")

	go func() {
		<-raptorApp.Done()
		s.Shutdown(raptorApp.Err())
	}()

	s.AttachShutdownSignalHandler(sigCh)

	return s, nil

}
