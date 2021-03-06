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

package handler

import (
	"github.com/Rightech/ric-edge/third_party/go-ble/ble"
	"github.com/Rightech/ric-edge/third_party/go-ble/ble/linux"
)

func New() (Service, error) {
	dev, err := linux.NewDeviceWithName("ble-connector")
	if err != nil {
		return Service{}, err
	}

	return Service{dev: dev, conns: make(map[string]ble.Client)}, nil
}
