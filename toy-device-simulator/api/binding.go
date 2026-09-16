package api

import "toy-device-simulator/config"

// 挂靠（binding）：把设备册里的一条和配置树上的一个 环境/厂商/设备类型 合起来，
// 得到这一次运行真正用的配置。Phase 11 之前这三样是建设备时就焊死在设备定义里的，
// 现在推迟到 start —— 同一台设备可以今天挂 A 类型、明天挂 B 类型。
//
// bindDevice 把已合成的设备属性挂上这一次运行的身份三级与 server.url。
// 产品默认值与临时覆盖在 composeDevice 里先叠好，再交给这里写身份。
//
// 一台设备一次只能挂一处：真机不可能同时以两种机型在线，而且租约已经保证同一台
// 设备同时只被一个 run 用。所以 s.devices 仍然以 device_id 为 key。
func bindDevice(book config.Device, envName, enterprise, deviceType, serverURL string) config.Device {
	_ = envName // 环境名不进 config.Device，它只活在 managedDevice.envName
	bound := book
	bound.Enterprise = enterprise
	bound.DeviceType = deviceType
	bound.Server.URL = serverURL
	return bound
}
