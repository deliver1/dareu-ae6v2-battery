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
	VendorID        uint16 = 0x260D // 达尔优官方 VID (9741)
	ProductID       uint16 = 0x1261 // AE6 V2 专属 PID (4705)[cite: 6]
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

// 3x5 像素极简点阵字模 (0-9)，确保在小图标内绝对锐利清晰
var font3x5 = map[rune][5][3]bool{
	'0': {{true, true, true}, {true, false, true}, {true, false, true}, {true, false, true}, {true, true, true}},
	'1': {{false, true, false}, {true, true, false}, {false, true, false}, {false, true, false}, {true, true, true}},
	'2': {{true, true, true}, {false, false, true}, {true, true, true}, {true, false, false}, {true, true, true}},
	'3': {{true, true, true}, {false, false, true}, {true, true, true}, {false, false, true}, {true, true, true}},
	'4': {{true, false, true}, {true, false, true}, {true, true, true}, {false, false, true}, {false, false, true}},
	'5': {{true, true, true}, {true, false, false}, {true, true, true}, {false, false, true}, {true, true, true}},
	'6': {{true, true, true}, {true, false, false}, {true, true, true}, {true, false, true}, {true, true, true}},
	'7': {{true, true, true}, {false, false, true}, {false, false, true}, {false, false, true}, {false, false, true}},
	'8': {{true, true, true}, {true, false, true}, {true, true, true}, {true, false, true}, {true, true, true}},
	'9': {{true, true, true}, {true, false, true}, {true, true, true}, {false, false, true}, {true, true, true}},
	'?': {{true, true, true}, {false, false, true}, {false, true, true}, {false, false, false}, {false, true, false}},
}

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetTitle("达尔优 AE6 V2")
	systray.SetIcon(createPhoneStyleBatteryIco(100))
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
		systray.SetTooltip(fmt.Sprintf("达尔优 AE6 V2 鼠标: 剩余 %d%%", battery))
		mStatus.SetTitle(fmt.Sprintf("当前电量: %d%%", battery))
		systray.SetIcon(createPhoneStyleBatteryIco(battery))

		if battery <= LowBattery {
			if !hasNotified {
				showLowBatteryNotification(battery)
				hasNotified = true
			}
		} else {
			hasNotified = false
		}
	} else {
		systray.SetTooltip("达尔优 AE6 V2: 未连接 (请插紧接收器或关闭网页驱动)")
		mStatus.SetTitle("状态: 接收器未连接/被占用")
		systray.SetIcon(createPhoneStyleBatteryIco(-1))
	}
}

func showLowBatteryNotification(level int) {
	notification := toast.Notification{
		AppID:   "达尔优鼠标电量助手",
		Title:   "鼠标电量低警告",
		Message: fmt.Sprintf("达尔优 AE6 V2 剩余电量仅剩 %d%%，请连接线缆充电！", level),
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

func getBatteryLevel() int {
	devices := hid.Enumerate(VendorID, ProductID)
	if len(devices) == 0 {
		return -1
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
			if n > BatteryByteIdx {
				val := int(buf[BatteryByteIdx])
				if val > 0 && val <= 100 {
					return val
				}
			}
			for i := 0; i < n; i++ {
				if buf[i] > 0 && buf[i] <= 100 {
					return int(buf[i])
				}
			}
		}
	}

	return -1
}

// 绘制手机样式的电池图标（电池框 + 内部像素点阵实时数字）
func createPhoneStyleBatteryIco(percent int) []byte {
	const (
		w = 32
		h = 32
	)
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(m, m.Bounds(), image.Transparent, image.Point{}, draw.Src)

	// 电池外壳边框颜色
	borderColor := color.RGBA{220, 220, 220, 255}
	tipColor := color.RGBA{180, 180, 180, 255}

	// 1. 绘制电池大外框 (X: 1 到 27, Y: 8 到 23)
	for x := 1; x <= 27; x++ {
		m.Set(x, 8, borderColor)
		m.Set(x, 23, borderColor)
	}
	for y := 8; y <= 23; y++ {
		m.Set(1, y, borderColor)
		m.Set(27, y, borderColor)
	}
	// 电池正极触头
	for y := 12; y <= 19; y++ {
		m.Set(28, y, tipColor)
		m.Set(29, y, tipColor)
	}

	// 2. 绘制电量与数字
	if percent >= 0 {
		pct := percent
		if pct > 100 {
			pct = 100
		}

		// 根据电量决定填充色（低电红色，充足绿色）
		var fillColor color.RGBA
		if pct <= 20 {
			fillColor = color.RGBA{255, 69, 58, 255} // 红
		} else {
			fillColor = color.RGBA{48, 209, 88, 255} // 绿
		}

		// 底层电量填充条 (横向充填)
		fillW := int(float64(24) * (float64(pct) / 100.0))
		for x := 2; x < 2+fillW; x++ {
			for y := 9; y <= 22; y++ {
				m.Set(x, y, fillColor)
			}
		}

		// 3. 在中心绘制 3x5 点阵数字（黑色反差阴影或纯白显示，确保任何背景都清晰）
		numStr := fmt.Sprintf("%d", pct)
		if pct == 100 {
			numStr = "99" // 32x32 宽度限制，100% 紧凑显示 99 或满格
		}

		// 计算点阵居中起始坐标
		totalWidth := len(numStr)*3 + (len(numStr)-1)*1
		startX := (28 - totalWidth) / 2
		startY := 13

		numColor := color.RGBA{0, 0, 0, 255} // 黑色数字在彩色电池上最醒目
		if pct <= 20 {
			numColor = color.RGBA{255, 255, 255, 255} // 红底下用白色数字
		}

		curX := startX
		for _, ch := range numStr {
			grid, ok := font3x5[ch]
			if ok {
				for r := 0; r < 5; r++ {
					for c := 0; c < 3; c++ {
						if grid[r][c] {
							m.Set(curX+c, startY+r, numColor)
						}
					}
				}
			}
			curX += 4 // 3像素宽度 + 1像素字间距
		}
	} else {
		// 未连接时：内部显示黄色问号
		qGrid := font3x5['?']
		qColor := color.RGBA{255, 204, 0, 255}
		for r := 0; r < 5; r++ {
			for c := 0; c < 3; c++ {
				if qGrid[r][c] {
					m.Set(13+c, 13+r, qColor)
				}
			}
		}
	}

	// 编码为 PNG 后封装为 Windows 原生 ICO
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
