package hotreloading

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/rapidcore/standalone"
)

func RunHotReloadingListener(server standalone.InteropServer, targetPaths []string, ctx context.Context, fileWatcherStrategy string) {
	if len(targetPaths) == 1 && targetPaths[0] == "" {
		log.Debugln("Hot reloading disabled.")
		return
	}
	defaultDebouncingDuration := 500 * time.Millisecond
	log.Infoln("Hot reloading enabled, starting filewatcher.", targetPaths)
	changeListener, err := NewChangeListener(defaultDebouncingDuration, fileWatcherStrategy)
	if err != nil {
		log.Errorln("Hot reloading disabled due to change listener error.", err)
		return
	}
	defer changeListener.Close()
	go changeListener.Start()
	changeListener.AddTargetPaths(targetPaths)
	go ResetListener(changeListener.debouncedChannel, server)

	<-ctx.Done()
	log.Infoln("Closing down filewatcher.")

}
