/**
 * Copyright 2019 Rightech IoT. All Rights Reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package mqtt

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Rightech/ric-edge/pkg/log/logger"
	"github.com/Rightech/ric-edge/pkg/store/mqtt"
	paho "github.com/eclipse/paho.mqtt.golang"
	log "github.com/sirupsen/logrus"
)

type Service struct {
	cli    paho.Client
	rpc    rpc
	toSend <-chan []byte
}

type SendPayload struct {
	Topic   string
	Payload []byte
}

const (
	requestTopic  = "ric-edge/+/command" // + - connector type
	responseTopic = "ric-edge/%s/response"
	stateTopic    = "ric-edge/sys/state"
	qos           = 1
)

type rpc interface {
	Call(string, []byte) []byte
}

func New(u, clientID, cert, key string, db mqtt.DB, cli rpc, sCh <-chan []byte) (Service, error) {
	parsedURL, err := url.Parse(u)
	if err != nil {
		return Service{}, err
	}

	if parsedURL.Host == "" {
		parsedURL.Host = parsedURL.Scheme + ":" + parsedURL.Opaque
		parsedURL.Opaque = ""
	}

	parsedURL.Scheme = "tcp"

	var certs []tls.Certificate

	if cert != "" && key != "" {
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return Service{}, err
		}

		certs = []tls.Certificate{pair}

		parsedURL.Scheme = "tls"
	}

	paho.CRITICAL = logger.New("critical", log.ErrorLevel)
	paho.ERROR = logger.New("error", log.DebugLevel)
	paho.WARN = logger.New("warn", log.DebugLevel)

	opts := paho.NewClientOptions().
		AddBroker(parsedURL.String()).
		SetClientID(clientID).
		SetAutoReconnect(true).
		SetStore(mqtt.NewStore(db)).
		SetCleanSession(false).
		SetKeepAlive(5 * time.Second).
		SetOrderMatters(false)

	if certs != nil {
		opts = opts.SetTLSConfig(&tls.Config{
			Certificates:       certs,
			InsecureSkipVerify: true,
		})

		log.Debug("mqtt tls enabled")
	}

	client := paho.NewClient(opts)
	token := client.Connect()
	if token.Wait() && token.Error() != nil {
		return Service{}, token.Error()
	}

	s := Service{client, cli, sCh}

	token = client.Subscribe(requestTopic, qos, s.rpcCallback)
	if token.Wait() && token.Error() != nil {
		return Service{}, token.Error()
	}

	go s.publishListener()

	log.Info("mqtt ready")

	return s, nil
}

func (s Service) publishListener() {
	for p := range s.toSend {
		err := s.publish(stateTopic, p)
		if err != nil {
			log.WithFields(log.Fields{
				"payload": string(p),
				"error":   err,
			}).Error("err publish state")
		}
	}
}

func (s Service) publish(topic string, payload []byte) error {
	token := s.cli.Publish(topic, qos, false, payload)
	if token.WaitTimeout(time.Minute) && token.Error() != nil {
		return token.Error()
	}

	return nil
}

func (s Service) rpcCallback(cli paho.Client, msg paho.Message) {
	connectorID := strings.Split(msg.Topic(), "/")[1]

	resp := s.rpc.Call(connectorID, msg.Payload())

	err := s.publish(fmt.Sprintf(responseTopic, connectorID), resp)
	if err != nil {
		log.WithFields(log.Fields{
			"response":  string(resp),
			"connector": connectorID,
			"request":   string(msg.Payload()),
			"error":     err,
		}).Error("err publish response")
	}
}

func (s Service) Close() error {
	s.cli.Disconnect(uint(time.Second / time.Millisecond))
	return nil
}
