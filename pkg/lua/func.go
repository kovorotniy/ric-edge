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

package lua

import (
	"encoding/binary"
	"strconv"

	jsoniter "github.com/json-iterator/go"
	lua "github.com/yuin/gopher-lua"
)

// binary
// --------------------------------------------------

func getBinaryOrder(ls *lua.LState) binary.ByteOrder {
	endian := "little"
	endianV := ls.Get(2)

	if endianV != lua.LNil {
		endianS, ok := endianV.(lua.LString)
		if !ok {
			ls.TypeError(2, lua.LTString)
			return nil
		}

		endian = endianS.String()
	}

	var order binary.ByteOrder

	switch endian {
	case "little":
		order = binary.LittleEndian
	case "big":
		order = binary.BigEndian
	default:
		ls.RaiseError("little or big endian allowed, but given: %s", endian)
		return nil
	}

	return order
}

func getSize(ls *lua.LState) int {
	size := 32
	sizeV := ls.Get(3)

	if sizeV != lua.LNil {
		sizeN, ok := sizeV.(lua.LNumber)
		if !ok {
			ls.TypeError(3, lua.LTNumber)
			return 0
		}

		size = int(sizeN)
	}

	switch size {
	case 16, 32, 64:
	default:
		ls.RaiseError("16, 32 or 64 size allowed, but given: %d", size)
		return 0
	}

	return size
}

func binaryToNumber(ls *lua.LState) int {
	bytes := []byte(ls.CheckString(1))

	order := getBinaryOrder(ls)
	if order == nil {
		return 0
	}

	size := getSize(ls)
	if size == 0 {
		return 0
	}

	bLen := size / 8

	if len(bytes) < bLen {
		ls.RaiseError("binary should have length at least: %d, but have: %d",
			bLen, len(bytes))
		return 0
	}

	var num lua.LNumber

	switch size {
	case 32:
		num = lua.LNumber(order.Uint32(bytes))
	case 16:
		num = lua.LNumber(order.Uint16(bytes))
	default:
		num = lua.LNumber(order.Uint64(bytes))
	}

	ls.Push(num)

	return 1
}

func numberToBinary(ls *lua.LState) int {
	d := ls.Get(1)

	var num lua.LNumber

	switch v := d.(type) {
	case lua.LNumber:
		num = v
	case lua.LString:
		fl, err := strconv.ParseFloat(v.String(), 64)
		if err != nil {
			ls.Push(lua.LNil)
			ls.Push(lua.LString(err.Error()))

			return 2
		}
		num = lua.LNumber(fl)
	default:
		ls.Push(lua.LNil)
		ls.Push(lua.LString("number or string required"))

		return 2
	}

	order := getBinaryOrder(ls)
	if order == nil {
		return 0
	}

	size := getSize(ls)
	if size == 0 {
		return 0
	}

	bLen := size / 8
	bytes := make([]byte, bLen)

	switch size {
	case 32:
		order.PutUint32(bytes, uint32(num))
	case 16:
		order.PutUint16(bytes, uint16(num))
	default:
		order.PutUint64(bytes, uint64(num))
	}

	ud := ls.NewUserData()
	ud.Value = bytes

	ls.Push(ud)

	return 1
}

// --------------------------------------------------
// json
// --------------------------------------------------

func fromJSON(ls *lua.LState) int {
	str := ls.CheckString(1)

	ud := ls.NewUserData()

	err := jsoniter.ConfigFastest.UnmarshalFromString(str, &ud.Value)
	if err != nil {
		ls.Push(lua.LNil)
		ls.Push(lua.LString(err.Error()))

		return 2
	}

	ls.Push(ud)

	return 1
}

// --------------------------------------------------
