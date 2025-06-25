package localstack

import (
	"bytes"
	"net/http"
	"net/url"

	"github.com/sirupsen/logrus"
)

type LocalStackClient struct {
	UpstreamEndpoint string
	RuntimeId        string
}

func NewLocalStackClient(endpoint, runtimeId string) *LocalStackClient {
	return &LocalStackClient{
		UpstreamEndpoint: endpoint,
		RuntimeId:        runtimeId,
	}
}

func (ls *LocalStackClient) sendCallback(namespace, invokeId, dest string, payload []byte) error {
	endpoint, err := url.JoinPath(ls.UpstreamEndpoint, namespace, invokeId, dest)
	if err != nil {
		return err
	}
	logrus.WithField("url", endpoint).WithField("invoke-id", invokeId).Debugf("Sending callback.")

	if _, err := http.Post(endpoint, "application/json", bytes.NewReader(payload)); err != nil {
		return err
	}

	return nil
}

func (ls *LocalStackClient) SendStatus(status LocalStackStatus, payload []byte) error {
	return ls.sendCallback("/status", ls.RuntimeId, string(status), payload)
}

func (ls *LocalStackClient) SendInvocation(invokeId, dest string, payload []byte) error {
	return ls.sendCallback("invocations", invokeId, dest, payload)
}
