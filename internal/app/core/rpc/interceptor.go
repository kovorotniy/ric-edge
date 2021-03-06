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

package rpc

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Rightech/ric-edge/internal/pkg/core/cloud"
	"github.com/Rightech/ric-edge/pkg/jsonrpc"
	"github.com/Rightech/ric-edge/pkg/nanoid"
	"github.com/Rightech/ric-edge/pkg/store/state"
	jsoniter "github.com/json-iterator/go"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/objx"
)

type stater interface {
	Get(string) map[string]interface{}
	Set(string, interface{}) error
}

type rpcCli interface {
	Call(name, id string, p []byte) <-chan []byte
}

type api interface {
	LoadModel(string) (cloud.Model, error)
	LoadObject(string) (cloud.Object, error)
}

type jober interface {
	AddFunc(string, func()) (int, error)
	Remove(int)
}

type action interface {
	Add(name, code string) error
	Remove(name string)
	Execute(name string, data interface{}) (interface{}, error)
}

type Service struct {
	rpc        rpcCli
	api        api
	job        jober
	jobs       []int
	action     action
	obj        cloud.Object
	model      cloud.Model
	timeout    time.Duration
	state      stater
	requestsCh <-chan []byte
	stateCh    chan<- []byte
}

func New(id string, tm time.Duration, ac action, db state.DB, cleanStart bool, r rpcCli,
	api api, j jober, stateCh chan<- []byte, requestsCh <-chan []byte) (Service, error) {
	st, err := state.NewService(db, cleanStart)
	if err != nil {
		return Service{}, err
	}

	object, err := api.LoadObject(id)
	if err != nil {
		return Service{}, err
	}

	model, err := api.LoadModel(object.Models.ID)
	if err != nil {
		return Service{}, err
	}

	for k, v := range model.Expressions() {
		err := ac.Add(k, v)
		if err != nil {
			return Service{}, err
		}
	}

	s := Service{r, api, j, make([]int, 0), ac, object, model, tm, st,
		requestsCh, stateCh}

	go s.requestsListener()

	return s, s.spawnJobs(model.Actions())
}

func (s Service) GetEdgeID() string {
	return s.obj.ID
}

func (s Service) undoAll() {
	for _, id := range s.jobs {
		s.job.Remove(id)
	}

	for k := range s.model.Expressions() {
		s.action.Remove(k)
	}

	// TODO:
	// 1. stop all subscriptions
	// 2. load new object and model
	// 3. spawn new actions
}

var (
	errTimeout   = jsonrpc.ErrServer.AddData("msg", "timeout")
	errUnmarshal = jsonrpc.ErrParse.AddData("msg", "json unmarshal error")
	errBadIDType = jsonrpc.ErrInternal.AddData("msg", "id should be string or null")
)

func (s Service) sendState(parent string, value interface{}) {
	data := make(objx.Map, 1)

	parent = strings.TrimPrefix(parent, "edge.")

	data.Set(parent, value)

	st, err := jsoniter.ConfigFastest.Marshal(data)
	if err != nil {
		log.WithFields(log.Fields{
			"value": value,
			"error": err,
		}).Error("sendState: marshal json")
	}

	s.stateCh <- st
}

func (s Service) requestsListener() { //nolint: funlen
	for msg := range s.requestsCh {
		var request objx.Map

		err := jsoniter.ConfigFastest.Unmarshal(msg, &request)
		if err != nil {
			log.WithFields(log.Fields{
				"value": string(msg),
				"error": err,
			}).Error("requestsListener: unmarshal json")

			continue
		}

		if request.Get("params.value").IsNil() {
			log.WithField("value", string(msg)).
				Error("requestsListener: empty value in notification")

			continue
		}

		parent := request.Get("params.__request_params._parent").Str()
		if parent == "" {
			log.WithField("value", string(msg)).
				Error("requestsListener: empty _parent in request")

			continue
		}

		log.WithFields(log.Fields{
			"parent": parent,
			"value":  request.Get("params.value").Data(),
		}).Debug("requestsListener: new notification")

		if request.Get("params.value").IsStr() {
			decoded, err := base64.StdEncoding.DecodeString(request.Get("params.value").Str())
			if err == nil {
				request.Set("params.value", decoded)
			}
		}

		res, err := s.action.Execute("read."+parent, request.Get("params.value").Data())
		if err != nil {
			log.WithFields(log.Fields{
				"value":  string(msg),
				"parent": parent,
				"error":  err,
			}).Debug("err: do action")
		}

		if res == nil {
			res = request.Get("params.value").Data()
		}

		err = s.state.Set(parent, res)
		if err != nil {
			log.WithFields(log.Fields{
				"value":  string(msg),
				"parent": parent,
				"error":  err,
			}).Error("requestsListener: set state")

			continue
		}

		s.sendState(parent, res)
	}
}

func (s Service) buildJobFn(v cloud.ActionConfig) func() {
	return func() {
		resp := s.Call(v.Connector, v.Payload)
		log.WithField("r", string(resp)).Debug("cron job response")
	}
}

func (s Service) subscribe(v cloud.ActionConfig) {
	resp := s.Call(v.Connector, v.Payload)

	idVal := jsoniter.ConfigFastest.Get(resp, "result").Get("process_id")
	if idVal.LastError() != nil {
		log.WithError(idVal.LastError()).Error("process_id not found")
		return
	}

	log.Debug("start subscribe with process_id: ", idVal.ToString())
}

func (s Service) spawnJobs(actions map[string]cloud.ActionConfig) error {
	for _, v := range actions {
		switch v.Type {
		case "schedule":
			id, err := s.job.AddFunc(v.Interval, s.buildJobFn(v))
			if err != nil {
				return fmt.Errorf("spawn [%s]: %w", v.ID, err)
			}

			s.jobs = append(s.jobs, id)
		case "subscribe":
			go s.subscribe(v)
		default:
			return errors.New("spawn: wrong type " + v.Type)
		}
	}

	return nil
}

func (s Service) fillTemplate(data []byte) ([]byte, bool, error) {
	begin := bytes.Index(data, []byte("{{"))
	if begin < 0 {
		return data, false, nil
	}

	end := bytes.Index(data[begin:], []byte("}}"))
	if end < 0 {
		return nil, false, errors.New("begin found but end not found")
	}

	end = begin + end + 2

	name := string(data[begin:end])
	beforeLen := len(name)

	name = strings.ReplaceAll(strings.Trim(name, " {}"), "object.config.", "")

	name = s.obj.Config.Get(name).Str()

	finLen := len(name)

	if finLen <= beforeLen {
		copy(data[begin:], name)
		copy(data[begin+len(name):], data[end:])

		res, _, err := s.fillTemplate(data[:len(data)-(beforeLen-finLen)])

		return res, true, err
	}

	after := data[end:]
	data = append(data[:begin], name...)
	data = append(data, after...)

	res, _, err := s.fillTemplate(data)

	return res, true, err
}

func (s Service) prepareRequest(payload []byte) ([]byte, objx.Map, *jsonrpc.Error) {
	payload, changed, err := s.fillTemplate(payload)
	if err != nil {
		e := errUnmarshal.AddData("err", err.Error())
		return nil, nil, &e
	}

	var data objx.Map

	err = jsoniter.ConfigFastest.Unmarshal(payload, &data)
	if err != nil {
		e := errUnmarshal.AddData("err", err.Error())
		return nil, nil, &e
	}

	id := data.Get("id")

	if !id.IsNil() && !id.IsStr() {
		e := errBadIDType.AddData("current_id", id.Data())
		return nil, nil, &e
	}

	if id.IsNil() {
		data.Set("id", nanoid.New())

		changed = true
	}

	if data.Get("params._type").Str() == "write" {
		parent := data.Get("params._parent").Str()
		if parent != "" {
			res, err := s.action.Execute("write."+parent, data.Get("params.value").Data())
			if err == nil {
				data.Set("params.value", res)

				changed = true
			}
		}
	}

	if changed {
		payload, err = jsoniter.ConfigFastest.Marshal(data)
		if err != nil {
			panic(err)
		}
	}

	return payload, data, nil
}

func (s Service) Call(name string, payload []byte) []byte {
	payload, data, err := s.prepareRequest(payload)
	if err != nil {
		return jsonrpc.BuildErrResp("", *err)
	}

	resultC := s.rpc.Call(name, data.Get("id").Str(), payload)
	timer := time.NewTimer(s.timeout)
	select {
	case msg := <-resultC:
		if !timer.Stop() {
			<-timer.C
		}

		if data.Get("params._type").Str() == "read" {
			msg = s.prepareResponse(data, msg)
		}

		return msg
	case <-timer.C:
		return jsonrpc.BuildErrResp(data.Get("id").Str(), errTimeout)
	}
}

func (s Service) prepareResponse(req objx.Map, resp []byte) []byte { //nolint: funlen
	parent := req.Get("params._parent").Str()
	if parent == "" {
		return resp
	}

	var result objx.Map

	err := jsoniter.ConfigFastest.Unmarshal(resp, &result)
	if err != nil {
		log.WithFields(log.Fields{
			"value":  string(resp),
			"method": req.Get("method").Str(),
			"error":  err,
		}).Error("updateState: unmarshal json")

		return resp
	}

	if result.Get("result").IsNil() {
		return resp
	}

	// skip if its notification payload
	if result.Get("result.notification").Bool() {
		return resp
	}

	if result.Get("result").IsStr() {
		decoded, err := base64.StdEncoding.DecodeString(result.Get("result").Str())
		if err == nil {
			result.Set("result", decoded)
		}
	}

	res, err := s.action.Execute("read."+parent, result.Get("result").Data())
	if err != nil {
		log.WithFields(log.Fields{
			"value":  string(resp),
			"parent": parent,
			"method": req.Get("method").Str(),
			"error":  err,
		}).Debug("err: do action")
	}

	if res == nil {
		res = result.Get("result").Data()
	}

	err = s.state.Set(parent, res)
	if err != nil {
		log.WithFields(log.Fields{
			"value":  string(resp),
			"parent": parent,
			"method": req.Get("method").Str(),
			"error":  err,
		}).Error("updateState: set")
	}

	s.sendState(parent, res)

	result.Set("result", res)

	data, err := jsoniter.ConfigFastest.Marshal(result)
	if err != nil {
		log.WithFields(log.Fields{
			"value":  string(resp),
			"method": req.Get("method").Str(),
			"error":  err,
			"result": result,
		}).Error("updateState: marshal result to json")

		return resp
	}

	return data
}
