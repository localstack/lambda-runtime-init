package main

import (
	"bytes"
	"encoding/json"
	log "github.com/sirupsen/logrus"
	"io"
	"net/http"
	"os"
	"strings"
)

// everything that was previously "stop_container" or similar => now its an internal "worker re-initialization"

func main() {
	// SETUP
	// os.Args
	runtimesEnv := os.Getenv("COMPATIBLE_RUNTIMES")
	archsEnv := os.Getenv("COMPATIBLE_ARCHITECTURES")
	lsEndpoint := "http://" + os.Getenv("LOCALSTACK_HOSTNAME") + ":" + os.Getenv("EDGE_PORT")

	// REGISTRATION
	registrationInfo := RegistrationInfo{
		CompatibleRuntimes:      strings.Split(runtimesEnv, ";"),
		CompatibleArchitectures: strings.Split(archsEnv, ";"),
	}
	workerId, err := registerWorker(lsEndpoint, registrationInfo)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		deregisterWorker(workerId)
	}()

	// LOOP
	cmdChan := setupCommandChannel()
	for cmd := range cmdChan {
		// switch based on event

		switch cmd {
		case "INIT":
			reinitializeExecutionEnvironment()

			startRapid()
		}

	}

}

func deregisterWorker(id string) {
	// TODO
}

func startRapid() {
	// TODO
}

func setupCommandChannel() chan string {
	// TODO
}

func registerWorker(endpoint string, info RegistrationInfo) (string, error) {
	marshalled, err := json.Marshal(info)
	if err != nil {
		return "", err
	}
	post, err := http.Post(endpoint+"/register", "application/json", bytes.NewReader(marshalled))
	if err != nil {
		return "", err
	}
	content, err := io.ReadAll(post.Body)
	if err != nil {
		return "", err
	}
	var response RegisterResponse
	if err = json.Unmarshal(content, &response); err != nil {
		return "", err
	}
	return response.WorkerId, nil
}

func reinitializeExecutionEnvironment() {
	// clear environment from side-effects (e.g. /tmp, /var/task, /opt, ...)
	// reset environment variables
}

func clearDirectory(clearPath string) error {
	// TODO
	return nil
}

// POST /worker

// GET /worker/{id}/command (blocking call, don't set a timeout)

// POST /worker/{id}/error
// POST /worker/{id}/exit

type RegistrationInfo struct {
	CompatibleRuntimes      []string `json:"compatibleRuntimes"`
	CompatibleArchitectures []string `json:"compatibleArchitectures"`
}

type RegisterResponse struct {
	WorkerId string `json:"workerId"`
}

type InitEvent struct {
	Environment map[string]string `json:"environment"`
}
