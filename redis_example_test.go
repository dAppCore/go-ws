// SPDX-Licence-Identifier: EUPL-1.2

package ws

import core "dappco.re/go"

func ExampleNewRedisBridge() {
	bridge, err := NewRedisBridge(nil, RedisConfig{})
	core.Println(bridge == nil, err != nil)
	// Output: true true
}

func ExampleRedisBridge_Start() {
	var bridge *RedisBridge
	r := bridge.Start(core.Background())
	core.Println(!r.OK)
	// Output: true
}

func ExampleRedisBridge_Stop() {
	var bridge *RedisBridge
	r := bridge.Stop()
	core.Println(r.OK)
	// Output: true
}

func ExampleRedisBridge_PublishToChannel() {
	var bridge *RedisBridge
	r := bridge.PublishToChannel("events", Message{Type: TypeEvent})
	core.Println(!r.OK)
	// Output: true
}

func ExampleRedisBridge_PublishBroadcast() {
	var bridge *RedisBridge
	r := bridge.PublishBroadcast(Message{Type: TypeEvent})
	core.Println(!r.OK)
	// Output: true
}

func ExampleRedisBridge_SourceID() {
	bridge := &RedisBridge{sourceID: "source-1"}
	core.Println(bridge.SourceID())
	// Output: source-1
}
