package bootstrap

import (
	"fmt"
	"os"

	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/interop"
	"golang.org/x/sys/unix"
)

const (
	optBootstrap     = "/opt/bootstrap"
	runtimeBootstrap = "/var/runtime/bootstrap"
)

func IsBootstrapFileExist(filePath string) bool {
	file, err := os.Stat(filePath)
	return !os.IsNotExist(err) && !file.IsDir()
}

func GetBootstrap(args []string) (interop.Bootstrap, string) {
	var bootstrapLookupCmd []string
	var handler string
	currentWorkingDir := "/var/task" // default value

	if len(args) <= 1 {
		// set default value to /var/task/bootstrap, but switch to the other options if it doesn't exist
		bootstrapLookupCmd = []string{
			fmt.Sprintf("%s/bootstrap", currentWorkingDir),
		}

		if !IsBootstrapFileExist(bootstrapLookupCmd[0]) {
			var bootstrapCmdCandidates = []string{
				optBootstrap,
				runtimeBootstrap,
			}

			for i, bootstrapCandidate := range bootstrapCmdCandidates {
				if IsBootstrapFileExist(bootstrapCandidate) {
					bootstrapLookupCmd = []string{bootstrapCmdCandidates[i]}
					break
				}
			}
		}

		// handler is used later to set an env var for Lambda Image support
		handler = ""
	} else if len(args) > 1 {

		bootstrapLookupCmd = args[1:]

		if cwd, err := os.Getwd(); err == nil {
			currentWorkingDir = cwd
		}

		if len(args) > 2 {
			// Assume last arg is the handler
			handler = args[len(args)-1]
		}

		log.Infof("exec '%s' (cwd=%s, handler=%s)", args[1], currentWorkingDir, handler)

	} else {
		log.Panic("insufficient arguments: bootstrap not provided")
	}

	err := unix.Access(bootstrapLookupCmd[0], unix.X_OK)
	if err != nil {
		log.Debug("Bootstrap not executable, setting permissions to 0755...", bootstrapLookupCmd[0])
		err = os.Chmod(bootstrapLookupCmd[0], 0755)
		if err != nil {
			log.Warn("Error setting bootstrap to 0755 permissions: ", bootstrapLookupCmd[0], err)
		}
	}

	return NewSimpleBootstrap(bootstrapLookupCmd, currentWorkingDir), handler
}
