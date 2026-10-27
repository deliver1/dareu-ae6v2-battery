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
	PollingInterval        = 30 * time.Second

	AppName    = "DareuAE6V2Tray"
	LowBattery = 15
)

type MouseState struct {
	Battery    int
	IsCharging bool
	Connected  bool
}

var (
	mStatus      *systray.MenuItem
	mAutoStart   *systray.MenuItem
	mQuit        *systray.MenuItem
	hasNotified  bool
	lastBattery  int = -1
)

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetTitle("达尔优 AE6 V2")
	systray.SetIcon(createCleanBatteryIco(100, false, true))
	systray.SetTooltip("达尔优 AE6 V2: 正在初始化...")

	// 托盘状态显示（类似弹出卡片的效果）
	mStatus = systray.AddMenuItem("状态: 读取中...", "当前鼠标电量与充电状态")
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
	time.Sleep(1 * time.Second)
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

		// 格式完全对齐图片设计：大字百分比 + 状态文字
		tooltip := fmt.Sprintf("%d %%\n%s · AE6 V2", state.Battery, statusText)
		systray.SetTooltip(tooltip)
		mStatus.SetTitle(fmt.Sprintf("电量: %d%% (%s)", state.Battery, statusText))

		// 底部托盘：纯净电池图标，充电时带闪电，底部不挤杂乱数字
		systray.SetIcon(createCleanBatteryIco(state.Battery, state.IsCharging, true))

		// 低电量提醒（非充电状态下）
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
		mStatus.SetTitle("状态: 接收器未连接")
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
	devices := hid.Enumerate(VendorID, ProductID)
	if len(devices) == 0 {
		return MouseState{Connected: false}
	}

	for _, devInfo := range devices {
		dev, err := devInfo.Open()
		if err != nil {
			continue
		}

		query := make([]byte, 65)
		query[0] = ReportID[cite: 4]
		_, _ = dev.Write(query)

		buf := make([]byte, 192)
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
			var batVal int = -1
			var isCharging bool = false

			// 提取电量百分比
			if n > BatteryByteIdx && buf[BatteryByteIdx] > 0 && buf[BatteryByteIdx] <= 100 {
				batVal = int(buf[BatteryByteIdx])
			} else {
				for i := 0; i < n; i++ {
					if buf[i] > 0 && buf[i] <= 100 {
						batVal = int(buf[i])
						break
					}
				}
			}

			// 检查充电状态位（通常紧随电量之后如 offset 19，或特定状态位）
			if n > BatteryByteIdx+1 {
				// 达尔优状态位：0 为放电，1 或 2 为插线充电
				if buf[BatteryByteIdx+1] == 1 || buf[BatteryByteIdx+1] == 2 {
					isCharging = true
				}
			}

			// 容错补充检测：如果电量在上升，也自动判定为充电
			if lastBattery != -1 && batVal > lastBattery {
				isCharging = true
			}
			if batVal != -1 {
				lastBattery = batVal
			}

			if batVal != -1 {
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

// 绘制原生精致电池托盘图标（纯电池条，不画多余数字，充电时带闪电）
func createCleanBatteryIco(percent int, isCharging bool, connected bool) []byte {
	const (
		w = 32
		h = 32
	)
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(m, m.Bounds(), image.Transparent, image.Point{}, draw.Src)

	// 亮银灰色外边框与触头（高对比度，暗色/浅色任务栏均清晰）
	borderCol := color.RGBA{220, 220, 220, 255}
	tipCol := color.RGBA{180, 180, 180, 255}

	// 1. 绘制电池外壳 (X: 3 到 26, Y: 9 到 23)
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
	// 正极凸起触头
	for y := 13; y <= 19; y++ {
		m.Set(27, y, tipCol)
		m.Set(28, y, tipCol)
	}

	if connected && percent >= 0 {
		pct := percent
		if pct > 100 {
			pct = 100
		}

		// 2. 电量条色彩：低电亮红，正常饱满鲜绿[cite: 19, 20]
		var fillCol color.RGBA
		if pct <= 20 && !isCharging {
			fillCol = color.RGBA{255, 69, 58, 255} // 低电红
		} else {
			fillCol = color.RGBA{46, 189, 89, 255} // 绿[cite: 19, 20]
		}

		fillW := int(float64(20) * (float64(pct) / 100.0))
		for x := 5; x < 5+fillW; x++ {
			for y := 11; y <= 21; y++ {
				m.Set(x, y, fillCol)
			}
		}

		// 3. 充电状态：在电池中心绘制专属红色/黄色闪电 ⚡ 标志[cite: 19, 20]
		if isCharging {
			// 闪电图标点阵
			boltCol := color.RGBA{255, 69, 58, 255} // 图中原版同款红橙色闪电[cite: 19, 20]
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
				m.Set(pt[0], pt[1], boltCol)
			}
		}
	} else {
		// 未连接时：内部显示黄色待机小横杠
		unColor := color.RGBA{255, 204, 0, 255}
		for x := 10; x <= 19; x++ {
			m.Set(x, 15, unColor)
			m.Set(x, 16, unColor)
		}
	}

	// 转换为 Windows 原生 ICO
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
