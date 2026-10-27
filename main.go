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
	"os/exec"
	"path/filepath"
	"strings"
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
	BatteryByteIdx  int    = 18
	PollingInterval        = 15 * time.Second

	AppName    = "DareuAE6V2Tray"
	LowBattery = 15
)

var CandidatePIDs = []uint16{
	0x1261, // 2.4G 接收器
	0x1260, // 有线模式
	0x04ED,
	0x0260,
	0x0001,
}

type MouseState struct {
	Battery    int
	IsCharging bool
	Connected  bool
}

var (
	mStatus     *systray.MenuItem
	mDebug      *systray.MenuItem
	mAutoStart  *systray.MenuItem
	mQuit       *systray.MenuItem
	hasNotified bool
	lastBattery int = -1
	lastLog     string = "尚未开始扫描"
	logMu       sync.Mutex
)

func setLog(msg string) {
	logMu.Lock()
	defer logMu.Unlock()
	lastLog = fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)
}

func getLog() string {
	logMu.Lock()
	defer logMu.Unlock()
	return lastLog
}

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

	mDebug = systray.AddMenuItem("导出排查日志到桌面", "生成 dareu_debug.txt 方便查看数据")

	mAutoStart = systray.AddMenuItemCheckbox("开机自启动", "随 Windows 开机启动", isAutoStartEnabled())

	systray.AddSeparator()

	mQuit = systray.AddMenuItem("退出程序", "退出")

	go handleMenuEvents()
	go pollingLoop()
}

func onExit() {}

func handleMenuEvents() {
	for {
		select {
		case <-mDebug.ClickedCh:
			go exportDebugFile()
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

func exportDebugFile() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	desktopPath := filepath.Join(home, "Desktop", "dareu_debug.txt")
	_ = os.WriteFile(desktopPath, []byte(getLog()), 0644)
	_ = exec.Command("notepad.exe", desktopPath).Start()
}

func pollingLoop() {
	time.Sleep(500 * time.Millisecond)
	updateBatteryState()

	ticker := time.NewTicker(PollingInterval)
	defer ticker.Stop()

	for range ticker.C {
		updateBatteryState()
	}
}

func updateBatteryState() {
	defer func() { _ = recover() }()

	state := fetchMouseState()

	if state.Connected {
		statusText := "使用中"
		if state.IsCharging {
			statusText = "充电中 ⚡"
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
		mStatus.SetTitle("状态: 接收器未连接/无数据")
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
		allDevs := hid.Enumerate(VendorID, 0)
		for _, d := range allDevs {
			matchedDevices = append(matchedDevices, d)
		}
	}

	if len(matchedDevices) == 0 {
		setLog("未找到任何达尔优设备 (VID: 0x260D)")
		return MouseState{Connected: false}
	}

	var debugLines []string

	for idx, devInfo := range matchedDevices {
		dev, err := devInfo.Open()
		if err != nil {
			debugLines = append(debugLines, fmt.Sprintf("[%d] PID:0x%04X 独占打不开", idx, devInfo.ProductID))
			continue
		}

		// 达尔优专用唤醒矩阵：组合发送 Feature / Output 指令
		// 指令 1: 0x08 0x02 触发状态上报
		pkt1 := make([]byte, 65)
		pkt1[0] = 0x08
		pkt1[1] = 0x02
		_, _ = dev.Write(pkt1)

		// 指令 2: 0x07 / 0x04 辅助触发指令
		pkt2 := make([]byte, 65)
		pkt2[0] = 0x08
		pkt2[1] = 0x08
		_, _ = dev.Write(pkt2)

		buf := make([]byte, 256)
		readCh := make(chan int, 1)
		go func() {
			defer func() { _ = recover() }()
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
			var hexParts []string
			limit := 24
			if n < limit {
				limit = n
			}
			for i := 0; i < limit; i++ {
				hexParts = append(hexParts, fmt.Sprintf("%02X", buf[i]))
			}
			hexStr := strings.Join(hexParts, " ")
			debugLines = append(debugLines, fmt.Sprintf("[%d] PID:0x%04X (长%d): %s", idx, devInfo.ProductID, n, hexStr))

			var batVal int = -1
			var isCharging bool = false

			// 优先检查固定偏移
			if n > BatteryByteIdx && buf[BatteryByteIdx] > 0 && buf[BatteryByteIdx] <= 100 {
				batVal = int(buf[BatteryByteIdx])
			} else {
				// 启发式搜索有效百分比
				for i := 1; i < n; i++ {
					if buf[i] >= 10 && buf[i] <= 100 {
						batVal = int(buf[i])
						break
					}
				}
			}

			// 充电状态探测
			if n > BatteryByteIdx+1 && (buf[BatteryByteIdx+1] == 1 || buf[BatteryByteIdx+1] == 2) {
				isCharging = true
			}
			// 某些固件下的充电状态在第 1 或第 2 字节
			if n > 2 && (buf[1] == 0x01 || buf[2] == 0x01 || buf[1] == 0x02) {
				isCharging = true
			}

			if batVal > 0 {
				lastBattery = batVal
				setLog(fmt.Sprintf("读取成功!\nPID:0x%04X 电量:%d%% 充电:%v\n前段字节: %s", devInfo.ProductID, batVal, isCharging, hexStr))
				return MouseState{
					Battery:    batVal,
					IsCharging: isCharging,
					Connected:  true,
				}
			}
		} else {
			debugLines = append(debugLines, fmt.Sprintf("[%d] PID:0x%04X 握手无响应", idx, devInfo.ProductID))
		}
	}

	setLog(strings.Join(debugLines, "\n"))
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
