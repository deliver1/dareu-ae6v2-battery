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
	"sync"
	"time"

	"github.com/getlantern/systray"
	"github.com/karalabe/hid"
	"golang.org/x/sys/windows/registry"
	"gopkg.in/toast.v1"
)

const (
	VendorID        uint16 = 0x260D
	ReportID        byte   = 0x08
	PollingInterval        = 3 * time.Second

	AppName    = "DareuAE6V2Tray"
	LowBattery = 15
)

var CandidatePIDs = []uint16{
	0x1261,
	0x04ED,
	0x1260,
}

type MouseState struct {
	Battery    int
	IsCharging bool
	Connected  bool
}

var (
	mStatus     *systray.MenuItem
	mAutoStart  *systray.MenuItem
	mQuit       *systray.MenuItem
	hasNotified bool
	scanMu      sync.Mutex
	uiMu        sync.Mutex
)

func main() {
	defer func() { _ = recover() }()
	systray.Run(onReady, onExit)
}

func onReady() {
	defer func() { _ = recover() }()

	systray.SetTitle("达尔优 AE6 V2")
	systray.SetIcon(createCleanBatteryIco(-1, false, false))
	systray.SetTooltip("达尔优 AE6 V2: 正在侦测...")

	mStatus = systray.AddMenuItem("状态: 读取中...", "当前电量状态")
	mStatus.Disable()

	systray.AddSeparator()

	autoTitle := "设为开机自启"
	if isAutoStartEnabled() {
		autoTitle = "取消开机自启"
	}
	mAutoStart = systray.AddMenuItem(autoTitle, "点击切换开机启动设置")

	systray.AddSeparator()

	mQuit = systray.AddMenuItem("退出程序", "关闭电量监控")

	go handleMenuEvents()
	go pollingLoop()
}

func onExit() {}

func handleMenuEvents() {
	defer func() { _ = recover() }()

	for {
		select {
		case <-mAutoStart.ClickedCh:
			if isAutoStartEnabled() {
				_ = setAutoStart(false)
				mAutoStart.SetTitle("设为开机自启")
			} else {
				_ = setAutoStart(true)
				mAutoStart.SetTitle("取消开机自启")
			}
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func pollingLoop() {
	time.Sleep(1 * time.Second)
	updateBatteryState()

	ticker := time.NewTicker(PollingInterval)
	defer ticker.Stop()

	for range ticker.C {
		updateBatteryState()
	}
}

func updateBatteryState() {
	defer func() { _ = recover() }()

	scanMu.Lock()
	state := fetchMouseState()
	scanMu.Unlock()

	uiMu.Lock()
	defer uiMu.Unlock()

	if state.Connected {
		statusText := "使用中"
		if state.IsCharging {
			if state.Battery >= 100 {
				statusText = "已充满 ⚡"
			} else {
				statusText = "充电中 ⚡"
			}
		}

		tooltip := fmt.Sprintf("%d %%\n%s · AE6 V2", state.Battery, statusText)
		systray.SetTooltip(tooltip)
		mStatus.SetTitle(fmt.Sprintf("电量: %d%% (%s)", state.Battery, statusText))

		systray.SetIcon(createCleanBatteryIco(state.Battery, state.IsCharging, true))

		if state.Battery <= LowBattery && !state.IsCharging {
			if !hasNotified {
				showLowBatteryNotification(state.Battery)
				hasNotified = true
			}
		} else {
			hasNotified = false
		}
	} else {
		systray.SetTooltip("未连接 · AE6 V2")
		mStatus.SetTitle("状态: 接收器未连接/休眠")
		systray.SetIcon(createCleanBatteryIco(-1, false, false))
	}
}

func showLowBatteryNotification(level int) {
	notification := toast.Notification{
		AppID:   "达尔优 AE6 V2",
		Title:   "鼠标电量不足",
		Message: fmt.Sprintf("当前电量仅剩 %d%%，请插上充电线！", level),
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

func fetchMouseState() MouseState {
	defer func() { _ = recover() }()

	var matchedDevices []hid.DeviceInfo
	for _, pid := range CandidatePIDs {
		devs := hid.Enumerate(VendorID, pid)
		if len(devs) > 0 {
			matchedDevices = append(matchedDevices, devs...)
		}
	}

	if len(matchedDevices) == 0 {
		return MouseState{Connected: false}
	}

	for _, devInfo := range matchedDevices {
		if devInfo.UsagePage == 0x01 {
			continue
		}

		dev, err := devInfo.Open()
		if err != nil {
			continue
		}

		req := make([]byte, 65)
		req[0] = ReportID
		req[1] = 0x01
		_, _ = dev.Write(req)

		buf := make([]byte, 256)
		ch := make(chan int, 1)

		go func(d *hid.Device) {
			defer func() { _ = recover() }()
			n, _ := d.Read(buf)
			ch <- n
		}(dev)

		var n int
		select {
		case n = <-ch:
		case <-time.After(40 * time.Millisecond):
			n = 0
		}

		dev.Close()

		if n > 0 {
			var batVal int = -1
			var isCharging bool = false

			offset := 0
			if buf[0] == 8 {
				offset = 0
			}

			if n >= 3 && buf[offset+2] == 2 {
				isCharging = true
				batVal = 100
			}

			if n >= 10 && buf[offset+5] == 255 && buf[offset+6] == 255 {
				batVal = 100
				isCharging = true
			}

			if batVal == -1 {
				for i := 1; i < n; i++ {
					if buf[i] > 0 && buf[i] <= 100 {
						batVal = int(buf[i])
						break
					}
				}
			}

			if n >= 187 {
				if buf[10] == 2 || buf[11] == 2 {
					isCharging = true
					batVal = 100
				}
			}

			if devInfo.ProductID == 0x1260 {
				isCharging = true
				if batVal == -1 {
					batVal = 100
				}
			}

			if batVal > 0 {
				return MouseState{
					Battery:    batVal,
					IsCharging: isCharging,
					Connected:  true,
				}
			}
		}
	}

	return MouseState{Connected: false}
}

func createCleanBatteryIco(percent int, isCharging bool, connected bool) []byte {
	const (
		w = 32
		h = 32
	)
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(m, m.Bounds(), image.Transparent, image.Point{}, draw.Src)

	borderCol := color.RGBA{220, 220, 220, 255}
	tipCol := color.RGBA{180, 180, 180, 255}

	for x := 3; x <= 26; x++ {
		m.Set(x, 9, borderCol)
		m.Set(x, 10, borderCol)
		m.Set(x, 22, borderCol)
		m.Set(x, 23, borderCol)
	}
	for y := 9; y <= 23; y++ {
		m.Set(3, y, borderCol)
		m.Set(4, y, borderCol)
		m.Set(25, y, borderCol)
		m.Set(26, y, borderCol)
	}
	for y := 13; y <= 19; y++ {
		m.Set(27, y, tipCol)
		m.Set(28, y, tipCol)
	}

	if connected && percent >= 0 {
		pct := percent
		if pct > 100 {
			pct = 100
		}

		var fillCol color.RGBA
		if pct <= 20 && !isCharging {
			fillCol = color.RGBA{255, 69, 58, 255}
		} else {
			fillCol = color.RGBA{46, 189, 89, 255}
		}

		fillW := int(float64(20) * (float64(pct) / 100.0))
		for x := 5; x < 5+fillW; x++ {
			for y := 11; y <= 21; y++ {
				m.Set(x, y, fillCol)
			}
		}

		if isCharging {
			boltCol := color.RGBA{255, 214, 10, 255}
			boltBorder := color.RGBA{0, 0, 0, 200}
			boltPixels := [][2]int{
				{16, 11},
				{15, 12}, {16, 12},
				{14, 13}, {15, 13}, {16, 13},
				{13, 14}, {14, 14}, {15, 14}, {16, 14}, {17, 14}, {18, 14},
				{14, 15}, {15, 15}, {16, 15},
				{14, 16}, {15, 16},
				{15, 17},
				{15, 18},
			}
			for _, pt := range boltPixels {
				m.Set(pt[0]-1, pt[1], boltBorder)
				m.Set(pt[0]+1, pt[1], boltBorder)
				m.Set(pt[0], pt[1], boltCol)
			}
		}
	} else {
		unColor := color.RGBA{255, 204, 0, 255}
		for x := 10; x <= 19; x++ {
			m.Set(x, 15, unColor)
			m.Set(x, 16, unColor)
		}
	}

	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, m)
	pngBytes := pngBuf.Bytes()

	icoBuf := new(bytes.Buffer)
	binary.Write(icoBuf, binary.LittleEndian, uint16(0))
	binary.Write(icoBuf, binary.LittleEndian, uint16(1))
	binary.Write(icoBuf, binary.LittleEndian, uint16(1))

	icoBuf.WriteByte(byte(w))
	icoBuf.WriteByte(byte(h))
	icoBuf.WriteByte(0)
	icoBuf.WriteByte(0)
	binary.Write(icoBuf, binary.LittleEndian, uint16(1))
	binary.Write(icoBuf, binary.LittleEndian, uint16(32))
	binary.Write(icoBuf, binary.LittleEndian, uint32(len(pngBytes)))
	binary.Write(icoBuf, binary.LittleEndian, uint32(22))

	icoBuf.Write(pngBytes)
	return icoBuf.Bytes()
}
