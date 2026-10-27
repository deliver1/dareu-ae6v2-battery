package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"github.com/getlantern/systray"
	"github.com/karalabe/hid"
	"golang.org/x/sys/windows/registry"
	"gopkg.in/toast.v1"
)

const (
	VendorID        uint16 = 0x260D // 达尔优官方 VID (9741)[cite: 6]
	ProductID       uint16 = 0x1261 // AE6 V2 接收器专属 PID (4705)[cite: 6]
	ReportID        byte   = 0x08   // 通信 Report ID[cite: 4, 6]
	BatteryByteIdx  int    = 18     // 电量百分比偏移位[cite: 6]
	PollingInterval        = 45 * time.Second

	AppName    = "DareuAE6V2Tray"
	LowBattery = 15
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
	systray.SetTitle("达尔优 AE6 V2")
	// 默认先赋予一个显眼的初始图标，防止空白透明
	systray.SetIcon(createIcoData(100))
	systray.SetTooltip("达尔优 AE6 V2: 正在连接...")

	mStatus = systray.AddMenuItem("状态: 读取中...", "当前鼠标连接与电量状态")
	mStatus.Disable()

	systray.AddSeparator()

	mAutoStart = systray.AddMenuItemCheckbox("开机自启动", "勾选后随系统启动", isAutoStartEnabled())

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
	// 初次启动延时 1 秒，等待系统托盘句柄完全就绪
	time.Sleep(1 * time.Second)
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
		systray.SetIcon(createIcoData(battery))

		if battery <= LowBattery {
			if !hasNotified {
				showLowBatteryNotification(battery)
				hasNotified = true
			}
		} else {
			hasNotified = false
		}
	} else {
		systray.SetTooltip("达尔优 AE6 V2: 未连接 (请确认插紧接收器或关闭网页驱动)")
		mStatus.SetTitle("状态: 接收器未连接/被占用")
		systray.SetIcon(createIcoData(-1))
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
	return err == nil && val != ""
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
	}
	_ = k.DeleteValue(AppName)
	return nil
}

// 遍历枚举并穿透匹配所有接收器接口
func getBatteryLevel() int {
	devices := hid.Enumerate(VendorID, ProductID)
	if len(devices) == 0 {
		return -1
	}

	// 逐个遍历接收器公开的所有 HID 接口，跳过被系统锁定的接口
	for _, devInfo := range devices {
		dev, err := devInfo.Open()
		if err != nil {
			continue // 被占用或独占，尝试下一个
		}

		// 触发查询
		query := make([]byte, 65)
		query[0] = ReportID[cite: 4]
		_, _ = dev.Write(query)

		buf := make([]byte, 192)
		// 带超时的非阻塞读取机制
		readCh := make(chan int, 1)
		go func() {
			n, _ := dev.Read(buf)
			readCh <- n
		}()

		var n int
		select {
		case n = <-readCh:
		case <-time.After(120 * time.Millisecond):
			n = 0
		}

		dev.Close()

		if n > 0 {
			// 1. 检查下标 18[cite: 6]
			if n > BatteryByteIdx {
				val := int(buf[BatteryByteIdx])
				if val > 0 && val <= 100 {
					return val
				}
			}
			// 2. 检查有效范围
			for i := 0; i < n; i++ {
				if buf[i] > 0 && buf[i] <= 100 {
					return int(buf[i])
				}
			}
		}
	}

	return -1
}

// 构建标准 Windows 原生 32x32 ICO 格式二进制数据（彻底解决透明空白图标问题）
func createIcoData(percent int) []byte {
	const (
		w = 32
		h = 32
	)
	m := image.NewRGBA(image.Rect(0, 0, w, h))

	// 背景保持全透明
	draw.Draw(m, m.Bounds(), image.Transparent, image.Point{}, draw.Src)

	// 电池外框与主体（高对比度）
	bodyBorder := color.RGBA{240, 240, 240, 255}
	tipColor := color.RGBA{220, 220, 220, 255}

	// 电池边框 [2, 7] 到 [26, 24]
	for x := 2; x <= 26; x++ {
		m.Set(x, 7, bodyBorder)
		m.Set(x, 8, bodyBorder)
		m.Set(x, 23, bodyBorder)
		m.Set(x, 24, bodyBorder)
	}
	for y := 7; y <= 24; y++ {
		m.Set(2, y, bodyBorder)
		m.Set(3, y, bodyBorder)
		m.Set(25, y, bodyBorder)
		m.Set(26, y, bodyBorder)
	}
	// 正极触头
	for y := 12; y <= 19; y++ {
		m.Set(27, y, tipColor)
		m.Set(28, y, tipColor)
	}

	if percent > 0 {
		pct := percent
		if pct > 100 {
			pct = 100
		}
		var fillCol color.RGBA
		if pct <= 20 {
			fillCol = color.RGBA{255, 59, 48, 255} // 低电亮红
		} else {
			fillCol = color.RGBA{52, 199, 89, 255} // 正常亮绿
		}

		fillW := int(float64(20) * (float64(pct) / 100.0))
		for x := 4; x < 4+fillW; x++ {
			for y := 10; y <= 21; y++ {
				m.Set(x, y, fillCol)
			}
		}
	} else {
		// 断开连接：中间亮黄色横杠
		unColor := color.RGBA{255, 204, 0, 255}
		for x := 8; x <= 20; x++ {
			m.Set(x, 15, unColor)
			m.Set(x, 16, unColor)
		}
	}

	// 编码为 PNG
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, m)
	pngBytes := pngBuf.Bytes()

	// 组装标准 22 字节 ICO 头
	icoBuf := new(bytes.Buffer)
	// ICONDIR
	binary.Write(icoBuf, binary.LittleEndian, uint16(0)) // 保留
	binary.Write(icoBuf, binary.LittleEndian, uint16(1)) // 1 = 图标
	binary.Write(icoBuf, binary.LittleEndian, uint16(1)) //
