package api

import "toy-device-simulator/config"

// 挂靠（binding）：把设备册里的一条和配置树上的一个 环境/厂商/设备类型 合起来，
// 得到这一次运行真正用的配置。Phase 11 之前这三样是建设备时就焊死在设备定义里的，
// 现在推迟到 start —— 同一台设备可以今天挂 A 类型、明天挂 B 类型。
//
// bindDevice 是这次合成的唯一入口，也是将来「产品类型」接进来的那道缝：等产品
// 类型开始规定音频格式、对话模式（按键/连续/唤醒词）和协议细节，就在这里多叠一层
// 产品属性，周围的调用方一行都不用改。
//
// 一台设备一次只能挂一处：真机不可能同时以两种机型在线，而且租约已经保证同一台
// 设备同时只被一个 run 用。所以 s.devices 仍然以 device_id 为 key。
func bindDevice(book config.Device, envName, enterprise, deviceType, serverURL string) config.Device {
	// ponytail: 产品类型的属性叠加点就在这一行之前——先按产品覆盖 book 的音频/
	// 对话模式字段，再写身份。现在没有产品类型，所以只写身份。
	_ = envName // 环境名不进 config.Device，它只活在 managedDevice.envName
	bound := book
	bound.Enterprise = enterprise
	bound.DeviceType = deviceType
	bound.Server.URL = serverURL
	return bound
}
