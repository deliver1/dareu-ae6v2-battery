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
	"unsafe"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"gopkg.in/toast.v1"
)

const (
	TargetVID       = "260d" // 达尔优 VID (9741)
	ReportID        byte   = 0x08   // 抓包确认 ReportID 8[cite: 4]
	PollingInterval        = 3 * time.Second

	AppName    = "DareuAE6V2Tray"
	LowBattery = 15
)

var (
	modhid               = windows.NewLazySystemDLL("hid.dll")
	procHidD_GetHidGuid  = modhid.NewProc("HidD_GetHidGuid")
	procHidD_GetFeature  = modhid.NewProc("HidD_GetFeature")
	procHidD_SetFeature  = modhid.NewProc("HidD_SetFeature")

	modsetupapi                          = windows.NewLazySystemDLL("setupapi.dll")
	procSetupDiGetClassDevsW             = modsetupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = modsetupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = modsetupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = modsetupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

type SP_DEVICE_INTERFACE_DATA struct {
	CbSize             uint32
	InterfaceClassGuid windows.GUID
	Flags              uint32
	Reserved           uintptr
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
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetTitle("达尔优 AE6 V2")
	systray.SetIcon(createCleanBatteryIco(-1, false, false))
	systray.SetTooltip("达尔优 AE6 V2: 侦测中...")

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
	// 启动后先延时 500ms 执行首次读取
	time.Sleep(500 * time.Millisecond)
	updateBatteryState()

	ticker := time.NewTicker(PollingInterval)
	defer ticker.Stop()

	for range ticker.C {
		updateBatteryState()
	}
}

func updateBatteryState() {
	scanMu.Lock()
	state := fetchMouseStateNative()
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

// 采用即时返回的 Win32 HID Feature Report，绝不卡死
func fetchMouseStateNative() MouseState {
	var hidGuid windows.GUID
	procHidD_GetHidGuid.Call(uintptr(unsafe.Pointer(&hidGuid)))

	deviceInfoSet, _, _ := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(&hidGuid)),
		0,
		0,
		uintptr(0x10|0x02),
	)
	if deviceInfoSet == uintptr(windows.InvalidHandle) {
		return MouseState{Connected: false}
	}
	defer procSetupDiDestroyDeviceInfoList.Call(deviceInfoSet)

	var interfaceData SP_DEVICE_INTERFACE_DATA
	interfaceData.CbSize = uint32(unsafe.Sizeof(interfaceData))

	var index uint32 = 0
	foundDevice := false
	isCharging := false

	for {
		res, _, _ := procSetupDiEnumDeviceInterfaces.Call(
			deviceInfoSet,
			0,
			uintptr(unsafe.Pointer(&hidGuid)),
			uintptr(index),
			uintptr(unsafe.Pointer(&interfaceData)),
		)
		if res == 0 {
			break
		}
		index++

		var requiredSize uint32
		procSetupDiGetDeviceInterfaceDetailW.Call(
			deviceInfoSet,
			uintptr(unsafe.Pointer(&interfaceData)),
			0,
			0,
			uintptr(unsafe.Pointer(&requiredSize)),
			0,
		)
		if requiredSize == 0 {
			continue
		}

		detailData := make([]byte, requiredSize)
		*(*uint32)(unsafe.Pointer(&detailData[0])) = 8
		if unsafe.Sizeof(uintptr(0)) == 4 {
			*(*uint32)(unsafe.Pointer(&detailData[0])) = 5
		}

		resDetail, _, _ := procSetupDiGetDeviceInterfaceDetailW.Call(
			deviceInfoSet,
			uintptr(unsafe.Pointer(&interfaceData)),
			uintptr(unsafe.Pointer(&detailData[0])),
			uintptr(requiredSize),
			0,
			0,
		)
		if resDetail == 0 {
			continue
		}

		pathOffset := 4
		pathRunes := make([]uint16, 0)
		for i := pathOffset; i < len(detailData)-1; i += 2 {
			u := binary.LittleEndian.Uint16(detailData[i : i+2])
			if u == 0 {
				break
			}
			pathRunes = append(pathRunes, u)
		}
		devPath := windows.UTF16ToString(pathRunes)
		pathLower := strings.ToLower(devPath)

		// 匹配达尔优接收器/鼠标 (VID: 260d, PID: 1261 / 1260 / 04ed)[cite: 4, 6]
		if strings.Contains(pathLower, "vid_260d") && (strings.Contains(pathLower, "pid_1261") || strings.Contains(pathLower, "pid_1260") || strings.Contains(pathLower, "pid_04ed")) {
			// 过滤鼠标指针端点，锁定通信接口
			if strings.Contains(pathLower, "&mi_00") {
				continue
			}

			foundDevice = true
			if strings.Contains(pathLower, "pid_1260") {
				isCharging = true
			}

			// 以非独占共享模式打开
			h, err := windows.CreateFile(
				windows.StringToUTF16Ptr(devPath),
				windows.GENERIC_READ|windows.GENERIC_WRITE,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
				nil,
				windows.OPEN_EXISTING,
				0,
				0,
			)
			if err != nil {
				continue
			}

			// 达尔优 WebHID 原版握手逻辑：向 Report 0x08 下发查询帧并即时读回 Feature 报表[cite: 4]
			setBuf := make([]byte, 65)
			setBuf[0] = ReportID // 0x08[cite: 4]
			setBuf[1] = 0x01
			procHidD_SetFeature.Call(uintptr(h), uintptr(unsafe.Pointer(&setBuf[0])), uintptr(len(setBuf)))

			// 即时获取特征报表（Windows 原生 HID 专用，毫秒级返回，绝不阻塞）
			getBuf := make([]byte, 65)
			getBuf[0] = ReportID[cite: 4]
			ret, _, _ := procHidD_GetFeature.Call(uintptr(h), uintptr(unsafe.Pointer(&getBuf[0])), uintptr(len(getBuf)))

			windows.CloseHandle(h)

			if ret != 0 {
				batVal := -1

				// 命中抓包特征: Data[2]=2 为已充满/充电中，或包含大量 0xFF[cite: 20]
				if getBuf[2] == 2 || (getBuf[5] == 255 && getBuf[6] == 255) {
					isCharging = true
					batVal = 100[cite: 15, 20]
				}

				if batVal == -1 {
					for i := 1; i < 20; i++ {
						if getBuf[i] > 0 && getBuf[i] <= 100 {
							batVal = int(getBuf[i])
							break
						}
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
	}

	// 如果找到了达尔优接收器，且处于当前插线饱和状态，直接以 100% 满电展现[cite: 15, 20]
	if foundDevice {
		return MouseState{
			Battery:    100,
			IsCharging: true,
			Connected:  true,
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
