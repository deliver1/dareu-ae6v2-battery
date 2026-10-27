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
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"
	"github.com/karalabe/hid"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"gopkg.in/toast.v1"
)

const (
	VendorID        uint16 = 0x260D // 达尔优官方 VID (9741)[cite: 6]
	ReportID        byte   = 0x08   // 抓包确认的 ReportID[cite: 4, 6]
	BatteryByteIdx  int    = 18     // 下标 18[cite: 6]
	PollingInterval        = 10 * time.Second // 充电时加快刷新频率 (10秒一次)

	AppName    = "DareuAE6V2Tray"
	LowBattery = 15
)

// 包含无线接收器与有线插线模式下的所有可能 PID[cite: 4, 6]
var CandidatePIDs = []uint16{
	0x1261, // 2.4G 接收器[cite: 6]
	0x1260, // 鼠标本体有线插线模式
	0x04ED, // 十进制 1261 兼容项[cite: 4]
	0x0260,
	0x0001,
}

type MouseState struct {
	Battery    int
	IsCharging bool
	Connected  bool
	RawBytes   string
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
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetTitle("达尔优 AE6 V2")
	systray.SetIcon(createCleanBatteryIco(-1, false, false))
	systray.SetTooltip("达尔优 AE6 V2: 正在侦测...")

	mStatus = systray.AddMenuItem("状态: 读取中...", "当前电量状态")
	mStatus.Disable()

	systray.AddSeparator()

	mDebug = systray.AddMenuItem("排查诊断信息", "点击查看 HID 数据包字节")

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
			msg := getLog()
			windows.MessageBox(0, windows.StringToUTF16Ptr(msg), windows.StringToUTF16Ptr("达尔优 AE6 V2 检测日志"), windows.MB_OK|windows.MB_ICONINFORMATION)
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
	time.Sleep(500 * time.Millisecond)
	updateBatteryState()

	ticker := time.NewTicker(PollingInterval)
	defer ticker.Stop()

	for range ticker.C {
		updateBatteryState()
	}
}

func updateBatteryState() {
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
		mStatus.SetTitle("状态: 接收器未连接/被占用")
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
	var matchedDevices []hid.DeviceInfo

	// 1. 扫描所有可能 PID（包含无线与有线）[cite: 4, 6]
	for _, pid := range CandidatePIDs {
		devs := hid.Enumerate(VendorID, pid)
		if len(devs) > 0 {
			matchedDevices = append(matchedDevices, devs...)
		}
	}

	// 2. 兜底扫描全部达尔优设备
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

	for idx, devInfo := range matchedDevices {
		dev, err := devInfo.Open()
		if err != nil {
			continue
		}

		// 发送达尔优心跳与电量触发包
		q1 := make([]byte, 65)
		q1[0] = ReportID
		q1[1] = 0x01
		_, _ = dev.Write(q1)

		q2 := make([]byte, 65)
		q2[0] = ReportID
		q2[1] = 0x02
		_, _ = dev.Write(q2)

		buf := make([]byte, 256)
		readCh := make(chan int, 1)
		go func() {
			n, _ := dev.Read(buf)
			readCh <- n
		}()

		var n int
		select {
		case n = <-readCh:
		case <-time.After(150 * time.Millisecond):
			n = 0
		}
		dev.Close()

		if n > 0 {
			var batVal int = -1
			var isCharging bool = false

			// 优先读取 offset 18[cite: 6]
			if n > BatteryByteIdx && buf[BatteryByteIdx] > 0 && buf[BatteryByteIdx] <= 100 {
				batVal = int(buf[BatteryByteIdx])
			} else {
				// 启发式搜索有效电量 (10~100)
				for i := 0; i < n; i++ {
					if buf[i] >= 10 && buf[i] <= 100 {
						batVal = int(buf[i])
						break
					}
				}
			}

			// 深度识别充电状态：
			// 1. 检查 offset 19 (充电状态位: 1 或 2)[cite: 6]
			if n > BatteryByteIdx+1 && (buf[BatteryByteIdx+1] == 1 || buf[BatteryByteIdx+1] == 2) {
				isCharging = true
			}
			// 2. 检查前导状态字 (达尔优有线充电时包头常带 0x01/0x02)
			if n > 2 && (buf[1] == 0x02 || buf[2] == 0x01) {
				isCharging = true
			}
			// 3. 有线模式 PID (0x1260) 默认即为插线充电中
			if devInfo.ProductID == 0x1260 {
				isCharging = true
			}
			// 4. 电量递增判定
			if lastBattery != -1 && batVal > lastBattery {
				isCharging = true
			}

			if batVal != -1 {
				lastBattery = batVal
				// 记录前 24 个字节十六进制，方便排查
				var hexStr []string
				limit := 24
				if n < limit {
					limit = n
				}
				for i := 0; i < limit; i++ {
					hexStr = append(hexStr, fmt.Sprintf("%02X", buf[i]))
				}
				setLog(fmt.Sprintf("接口[%d] PID:0x%04X\n电量:%d%% 充电:%v\n字节:%s", idx, devInfo.ProductID, batVal, isCharging, strings.Join(hexStr, " ")))

				return MouseState{
					Battery:    batVal,
					IsCharging: isCharging,
					Connected:  true,
				}
			}
		}
	}

	setLog("扫描了设备但未解析到有效电量数据")
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

		// 充电时在电池正中心点亮高对比度黄色闪电 ⚡
		if isCharging {
			boltCol := color.RGBA{255, 214, 10, 255} // 明亮金色闪电
			boltBorder := color.RGBA{0, 0, 0, 200}  // 黑色描边防吃色
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
