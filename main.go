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
	VendorID        uint16 = 0x260D // 达尔优官方 VID (9741)
	ReportID        byte   = 0x08   // 抓包确认 ReportId 严格为 8
	PollingInterval        = 8 * time.Second

	AppName    = "DareuAE6V2Tray"
	LowBattery = 15
)

var CandidatePIDs = []uint16{
	0x1261, // 接收器
	0x04ED, // 十进制 1261
	0x1260, // 有线模式
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
	lastLog     string = "就绪，等待获取数据..."
	logMu       sync.Mutex
	isScanning  bool
	scanMu      sync.Mutex
)

func setLog(msg string) {
	logMu.Lock()
	defer logMu.Unlock()
	lastLog = fmt.Sprintf("[%s]\n%s", time.Now().Format("15:04:05"), msg)
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
	systray.SetTooltip("达尔优 AE6 V2: 侦测中...")

	mStatus = systray.AddMenuItem("状态: 读取中...", "当前电量状态")
	mStatus.Disable()

	systray.AddSeparator()

	mDebug = systray.AddMenuItem("导出排查日志到桌面", "生成 dareu_debug.txt 查看数据")

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
	if isScanning {
		scanMu.Unlock()
		return
	}
	isScanning = true
	scanMu.Unlock()

	defer func() {
		scanMu.Lock()
		isScanning = false
		scanMu.Unlock()
	}()

	state := fetchMouseState()

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

// 精确对齐控制台 16 字节短报文特征[cite: 20]
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
		setLog("未找到达尔优 2.4G 接收器 (VID: 0x260D)")
		return MouseState{Connected: false}
	}

	var debugLines []string

	for idx, devInfo := range matchedDevices {
		// 跳过标准鼠标指针接口 (UsagePage 0x01, Usage 0x02)
		if devInfo.UsagePage == 0x01 && devInfo.Usage == 0x02 {
			continue
		}

		dev, err := devInfo.Open()
		if err != nil {
			continue
		}

		// 发送达尔优原厂激活帧 (ReportID 8)[cite: 20]
		req := make([]byte, 65)
		req[0] = ReportID
		req[1] = 0x01
		req[2] = 0x00
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
		case <-time.After(70 * time.Millisecond):
			n = 0
		}

		dev.Close()

		if n > 0 {
			var hexParts []string
			limit := 16
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

			// 匹配抓包数据特征：Data[0]=8, Data[2]=2 为饱和充满/充电[cite: 20]
			if n >= 4 {
				// 如果第一字节是 ReportID 8 或 0[cite: 20]
				offset := 0
				if buf[0] == 8 {
					offset = 0 // 驱动层直接包含了 ReportID[cite: 20]
				}

				// Data[2] 为 2 代表充电中/已充满[cite: 20]
				if buf[offset+2] == 2 {
					isCharging = true
					batVal = 100 // 充满饱和[cite: 15]
				}

				// 如果中间填充了大量 0xFF (255)，确认是 100% 充满态[cite: 20]
				if n >= 10 && buf[offset+5] == 255 && buf[offset+6] == 255 {
					batVal = 100
					isCharging = true
				}

				// 如果处于未插线放电状态，探测实际电量
				if batVal == -1 {
					for i := 1; i < n; i++ {
						if buf[i] > 0 && buf[i] <= 100 {
							batVal = int(buf[i])
							break
						}
					}
				}
			}

			// 如果是 187 字节长配置帧
			if n >= 187 {
				if buf[10] == 2 || buf[11] == 2 {
					isCharging = true
					batVal = 100
				}
			}

			// 有线模式直连判定
			if devInfo.ProductID == 0x1260 {
				isCharging = true
				if batVal == -1 {
					batVal = 100
				}
			}

			if batVal > 0 {
				lastBattery = batVal
				debugLines = append(debugLines, fmt.Sprintf("==> 成功锁定电量: %d%% (充电状态: %v)", batVal, isCharging))
				setLog(strings.Join(debugLines, "\n"))
				return MouseState{
					Battery:    batVal,
					IsCharging: isCharging,
					Connected:  true,
				}
			}
		}
	}

	if len(debugLines) > 0 {
		setLog(strings.Join(debugLines, "\n"))
	} else {
		setLog("所有厂商端口暂无回传")
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

		// 充电时在中心显示黄色闪电
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
