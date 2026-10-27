package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"github.com/getlantern/systray"
	"github.com/karalabe/hid"
	"golang.org/x/sys/windows/registry"
	"gopkg.in/toast.v1"
)

// 达尔优 AE6 V2 硬件参数
const (
	VendorID        uint16 = 0x260D // 达尔优官方 VID (9741)
	ProductID       uint16 = 0x1261 // AE6 V2 接收器专属 PID (4705)
	ReportID        byte   = 0x08   // 通信 Report ID
	BatteryByteIdx  int    = 18     // 截获配置数组中电量百分比的下标偏移
	PollingInterval        = 60 * time.Second

	AppName    = "DareuAE6V2Tray"
	LowBattery = 15 // 低于 15% 触发 Windows 弹窗警告
)

var (
	mStatus     *systray.MenuItem
	mAutoStart  *systray.MenuItem
	mQuit       *systray.MenuItem
	hasNotified bool
)

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetTitle("达尔优 AE6 V2 电量")
	systray.SetTooltip("正在初始化连接...")

	mStatus = systray.AddMenuItem("状态: 读取中...", "当前鼠标连接与电量状态")
	mStatus.Disable()

	systray.AddSeparator()

	mAutoStart = systray.AddMenuItemCheckbox("开机自启动", "勾选后随 Windows 开机静默启动", isAutoStartEnabled())

	systray.AddSeparator()

	mQuit = systray.AddMenuItem("退出程序", "关闭电量监控")

	go handleMenuEvents()
	go pollingLoop()
}

func onExit() {}

func handleMenuEvents() {
	for {
		select {
		case <-mAutoStart.ClickedCh:
			current := mAutoStart.Checked()
			if current {
				if err := setAutoStart(false); err == nil {
					mAutoStart.Uncheck()
				}
			} else {
				if err := setAutoStart(true); err == nil {
					mAutoStart.Check()
				}
			}
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func pollingLoop() {
	updateBattery()

	ticker := time.NewTicker(PollingInterval)
	defer ticker.Stop()

	for range ticker.C {
		updateBattery()
	}
}

func updateBattery() {
	battery := getBatteryLevel()

	if battery >= 0 {
		systray.SetTooltip(fmt.Sprintf("达尔优 AE6 V2: %d%%", battery))
		mStatus.SetTitle(fmt.Sprintf("当前电量: %d%%", battery))
		systray.SetIcon(generateBatteryIcon(battery))

		if battery <= LowBattery {
			if !hasNotified {
				showLowBatteryNotification(battery)
				hasNotified = true
			}
		} else {
			hasNotified = false
		}
	} else {
		systray.SetTooltip("达尔优 AE6 V2: 未连接或休眠中")
		mStatus.SetTitle("状态: 接收器未连接")
		systray.SetIcon(generateBatteryIcon(-1))
	}
}

func showLowBatteryNotification(level int) {
	notification := toast.Notification{
		AppID:   "达尔优鼠标电量助手",
		Title:   "鼠标电量不足警告",
		Message: fmt.Sprintf("达尔优 AE6 V2 剩余电量仅剩 %d%%，请及时连接线缆充电！", level),
	}
	_ = notification.Push()
}

func isAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()

	val, _, err := k.GetStringValue(AppName)
	if err != nil || val == "" {
		return false
	}
	return true
}

func setAutoStart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer k.Close()

	if enable {
		exePath, err := os.Executable()
		if err != nil {
			return err
		}
		exePath, err = filepath.Abs(exePath)
		if err != nil {
			return err
		}
		return k.SetStringValue(AppName, fmt.Sprintf("\"%s\"", exePath))
	} else {
		_ = k.DeleteValue(AppName)
		return nil
	}
}

func getBatteryLevel() int {
	devices := hid.Enumerate(VendorID, ProductID)
	if len(devices) == 0 {
		return -1
	}

	var targetDev *hid.DeviceInfo
	for _, dev := range devices {
		if dev.UsagePage != 0x01 {
			targetDev = &dev
			break
		}
	}
	if targetDev == nil {
		targetDev = &devices[0]
	}

	device, err := targetDev.Open()
	if err != nil {
		return -1
	}
	defer device.Close()

	// 发送查询报文触发设备回传数据
	queryPacket := make([]byte, 65)
	queryPacket[0] = ReportID
	_, _ = device.Write(queryPacket)

	buf := make([]byte, 192)
	readBytes, err := device.Read(buf)
	if err != nil || readBytes == 0 {
		return -1
	}

	// 优先检查已抓包确认的第 18 位下标
	if readBytes > BatteryByteIdx {
		val := int(buf[BatteryByteIdx])
		if val >= 0 && val <= 100 {
			return val
		}
	}

	// 容错匹配
	for i := 0; i < readBytes; i++ {
		if buf[i] > 0 && buf[i] <= 100 {
			return int(buf[i])
		}
	}

	return -1
}

func generateBatteryIcon(percent int) []byte {
	const (
		width  = 32
		height = 32
	)
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	transparent := color.RGBA{0, 0, 0, 0}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, transparent)
		}
	}

	borderColor := color.RGBA{230, 230, 230, 255}
	tipColor := color.RGBA{200, 200, 200, 255}

	for x := 3; x <= 27; x++ {
		img.Set(x, 8, borderColor)
		img.Set(x, 24, borderColor)
	}
	for y := 8; y <= 24; y++ {
		img.Set(3, y, borderColor)
		img.Set(27, y, borderColor)
	}
	for y := 13; y <= 19; y++ {
		img.Set(28, y, tipColor)
		img.Set(29, y, tipColor)
	}

	if percent > 0 {
		pct := percent
		if pct > 100 {
			pct = 100
		}
		var fillColor color.RGBA
		if pct <= 20 {
			fillColor = color.RGBA{255, 69, 58, 255}
		} else {
			fillColor = color.RGBA{48, 209, 88, 255}
		}

		fillWidth := int(float64(22) * (float64(pct) / 100.0))
		for x := 5; x < 5+fillWidth; x++ {
			for y := 10; y <= 22; y++ {
				img.Set(x, y, fillColor)
			}
		}
	} else if percent == -1 {
		disconnectColor := color.RGBA{150, 150, 150, 255}
		for x := 10; x <= 20; x++ {
			img.Set(x, 16, disconnectColor)
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
