#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
外设电量监控 — 精简双设备版（达尔优 AE6 V2 + Xbox 手柄）
纯标准库 + ctypes + tkinter，零第三方依赖
"""
from __future__ import annotations

import ctypes
import ctypes.wintypes as wt
import dataclasses
import math
import os
import subprocess
import sys
import threading
import time
import tkinter as tk
from typing import Optional

UI_SCALE = 1.0

# ===========================================================================
# Win32 API 基础签名与 64 位类型安全声明
# ===========================================================================
kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
user32 = ctypes.WinDLL("user32", use_last_error=True)
setupapi = ctypes.WinDLL("setupapi", use_last_error=True)
hid = ctypes.WinDLL("hid", use_last_error=True)
shell32 = ctypes.WinDLL("shell32", use_last_error=True)
gdi32 = ctypes.WinDLL("gdi32", use_last_error=True)

# 动态加载 XInput（Xbox 手柄支持）
try:
    xinput = ctypes.WinDLL("xinput1_4.dll")
except Exception:
    try:
        xinput = ctypes.WinDLL("xinput1_3.dll")
    except Exception:
        xinput = None

INVALID_HANDLE_VALUE = wt.HANDLE(-1).value
GENERIC_READ = 0x80000000
GENERIC_WRITE = 0x40000000
FILE_SHARE_RW = 0x00000001 | 0x00000002
OPEN_EXISTING = 3
FILE_FLAG_OVERLAPPED = 0x40000000
ERROR_IO_PENDING = 997
WAIT_TIMEOUT = 0x102

kernel32.CreateFileW.restype = wt.HANDLE
kernel32.CreateFileW.argtypes = [wt.LPCWSTR, wt.DWORD, wt.DWORD, ctypes.c_void_p, wt.DWORD, wt.DWORD, wt.HANDLE]
kernel32.CloseHandle.argtypes = [wt.HANDLE]
kernel32.WriteFile.argtypes = [wt.HANDLE, ctypes.c_void_p, wt.DWORD, ctypes.POINTER(wt.DWORD), ctypes.c_void_p]
kernel32.ReadFile.argtypes = [wt.HANDLE, ctypes.c_void_p, wt.DWORD, ctypes.POINTER(wt.DWORD), ctypes.c_void_p]
kernel32.CreateEventW.restype = wt.HANDLE
kernel32.CreateEventW.argtypes = [ctypes.c_void_p, wt.BOOL, wt.BOOL, wt.LPCWSTR]
kernel32.WaitForSingleObject.argtypes = [wt.HANDLE, wt.DWORD]
kernel32.GetOverlappedResult.argtypes = [wt.HANDLE, ctypes.c_void_p, ctypes.POINTER(wt.DWORD), wt.BOOL]
kernel32.CancelIoEx.argtypes = [wt.HANDLE, ctypes.c_void_p]
kernel32.GetModuleHandleW.restype = wt.HMODULE
kernel32.GetModuleHandleW.argtypes = [wt.LPCWSTR]
kernel32.CreateMutexW.restype = wt.HANDLE
kernel32.CreateMutexW.argtypes = [ctypes.c_void_p, wt.BOOL, wt.LPCWSTR]

class GUID(ctypes.Structure):
    _fields_ = [("Data1", wt.DWORD), ("Data2", wt.WORD), ("Data3", wt.WORD), ("Data4", ctypes.c_ubyte * 8)]
    def __init__(self, s: str = ""):
        super().__init__()
        s = (s or "{00000000-0000-0000-0000-000000000000}").strip().strip("{}")
        parts = s.split("-", 3)
        self.Data1 = int(parts[0], 16)
        self.Data2 = int(parts[1], 16)
        self.Data3 = int(parts[2], 16)
        rest = bytes.fromhex(parts[3].replace("-", ""))
        for i in range(8):
            self.Data4[i] = rest[i]

class HIDD_ATTRIBUTES(ctypes.Structure):
    _fields_ = [("Size", wt.ULONG), ("VendorID", wt.USHORT), ("ProductID", wt.USHORT), ("VersionNumber", wt.USHORT)]

class HIDP_CAPS(ctypes.Structure):
    _fields_ = [
        ("Usage", wt.USHORT), ("UsagePage", wt.USHORT),
        ("InputReportByteLength", wt.USHORT), ("OutputReportByteLength", wt.USHORT),
        ("FeatureReportByteLength", wt.USHORT), ("Reserved", wt.USHORT * 17),
        ("NumberLinkCollectionNodes", wt.USHORT), ("NumberInputButtonCaps", wt.USHORT),
        ("NumberInputValueCaps", wt.USHORT), ("NumberInputDataIndices", wt.USHORT),
        ("NumberOutputButtonCaps", wt.USHORT), ("NumberOutputValueCaps", wt.USHORT),
        ("NumberOutputDataIndices", wt.USHORT), ("NumberFeatureButtonCaps", wt.USHORT),
        ("NumberFeatureValueCaps", wt.USHORT), ("NumberFeatureDataIndices", wt.USHORT),
    ]

class SP_DEVICE_INTERFACE_DATA(ctypes.Structure):
    _fields_ = [("cbSize", wt.DWORD), ("InterfaceClassGuid", GUID), ("Flags", wt.DWORD), ("Reserved", ctypes.c_void_p)]

setupapi.SetupDiGetClassDevsW.restype = wt.HANDLE
setupapi.SetupDiGetClassDevsW.argtypes = [ctypes.POINTER(GUID), wt.LPCWSTR, wt.HWND, wt.DWORD]
setupapi.SetupDiEnumDeviceInterfaces.argtypes = [wt.HANDLE, ctypes.c_void_p, ctypes.POINTER(GUID), wt.DWORD, ctypes.POINTER(SP_DEVICE_INTERFACE_DATA)]
setupapi.SetupDiGetDeviceInterfaceDetailW.argtypes = [wt.HANDLE, ctypes.POINTER(SP_DEVICE_INTERFACE_DATA), ctypes.c_void_p, wt.DWORD, ctypes.POINTER(wt.DWORD), ctypes.c_void_p]
setupapi.SetupDiDestroyDeviceInfoList.argtypes = [wt.HANDLE]

hid.HidD_GetAttributes.argtypes = [wt.HANDLE, ctypes.POINTER(HIDD_ATTRIBUTES)]
hid.HidD_GetPreparsedData.argtypes = [wt.HANDLE, ctypes.POINTER(ctypes.c_void_p)]
hid.HidD_FreePreparsedData.argtypes = [ctypes.c_void_p]
hid.HidP_GetCaps.argtypes = [ctypes.c_void_p, ctypes.POINTER(HIDP_CAPS)]

GUID_DEVINTERFACE_HID = GUID("{4D1E55B2-F16F-11CF-88CB-001111000030}")

@dataclasses.dataclass
class HidIface:
    path: str
    vid: int
    pid: int
    usage_page: int
    usage: int
    input_len: int
    output_len: int

def _open_path(path: str, access: int, overlapped: bool = False) -> int:
    flags = FILE_FLAG_OVERLAPPED if overlapped else 0
    return kernel32.CreateFileW(path, access, FILE_SHARE_RW, None, OPEN_EXISTING, flags, None)

def enumerate_hid() -> list[HidIface]:
    hdev = setupapi.SetupDiGetClassDevsW(ctypes.byref(GUID_DEVINTERFACE_HID), None, None, 0x12)
    if hdev == INVALID_HANDLE_VALUE or not hdev:
        return []
    out: list[HidIface] = []
    try:
        idx = 0
        while True:
            did = SP_DEVICE_INTERFACE_DATA()
            did.cbSize = ctypes.sizeof(did)
            if not setupapi.SetupDiEnumDeviceInterfaces(hdev, None, ctypes.byref(GUID_DEVINTERFACE_HID), idx, ctypes.byref(did)):
                break
            idx += 1
            need = wt.DWORD(0)
            setupapi.SetupDiGetDeviceInterfaceDetailW(hdev, ctypes.byref(did), None, 0, ctypes.byref(need), None)
            if not need.value:
                continue
            buf = ctypes.create_string_buffer(need.value)
            ctypes.cast(buf, ctypes.POINTER(wt.DWORD))[0] = 8
            if not setupapi.SetupDiGetDeviceInterfaceDetailW(hdev, ctypes.byref(did), buf, need.value, ctypes.byref(need), None):
                continue
            path = ctypes.wstring_at(ctypes.addressof(buf) + 4)
            h = _open_path(path, 0)
            if not h or h == INVALID_HANDLE_VALUE:
                continue
            try:
                attrs = HIDD_ATTRIBUTES()
                attrs.Size = ctypes.sizeof(attrs)
                hid.HidD_GetAttributes(h, ctypes.byref(attrs))
                caps = HIDP_CAPS()
                pp = ctypes.c_void_p()
                if hid.HidD_GetPreparsedData(h, ctypes.byref(pp)):
                    hid.HidP_GetCaps(pp, ctypes.byref(caps))
                    hid.HidD_FreePreparsedData(pp)
                out.append(HidIface(
                    path=path, vid=attrs.VendorID, pid=attrs.ProductID,
                    usage_page=caps.UsagePage, usage=caps.Usage,
                    input_len=caps.InputReportByteLength,
                    output_len=caps.OutputReportByteLength))
            finally:
                kernel32.CloseHandle(h)
    finally:
        setupapi.SetupDiDestroyDeviceInfoList(hdev)
    return out

class OVERLAPPED(ctypes.Structure):
    _fields_ = [("Internal", ctypes.c_void_p), ("InternalHigh", ctypes.c_void_p), ("Offset", wt.DWORD), ("OffsetHigh", wt.DWORD), ("hEvent", wt.HANDLE)]

class HidDevice:
    def __init__(self, path: str, write: bool = False):
        self.path = path
        access = GENERIC_READ | (GENERIC_WRITE if write else 0)
        self.h = _open_path(path, access)
        self.rh = _open_path(path, GENERIC_READ, overlapped=True)

    def close(self):
        for handle in (self.rh, self.h):
            if handle and handle != INVALID_HANDLE_VALUE:
                kernel32.CloseHandle(handle)
        self.rh = 0
        self.h = 0

    def write_output(self, data: bytes) -> int:
        if not self.h or self.h == INVALID_HANDLE_VALUE:
            return 0
        buf = ctypes.create_string_buffer(data, len(data))
        written = wt.DWORD(0)
        ok = kernel32.WriteFile(self.h, buf, len(data), ctypes.byref(written), None)
        return written.value if ok else 0

    def read_input(self, length: int, timeout_ms: int = 150) -> Optional[bytes]:
        if not self.rh or self.rh == INVALID_HANDLE_VALUE:
            return None
        ev = kernel32.CreateEventW(None, True, False, None)
        if not ev:
            return None
        try:
            ov = OVERLAPPED()
            ov.hEvent = ev
            buf = ctypes.create_string_buffer(length)
            got = wt.DWORD(0)
            ok = kernel32.ReadFile(self.rh, buf, length, ctypes.byref(got), ctypes.byref(ov))
            if not ok and ctypes.get_last_error() != ERROR_IO_PENDING:
                return None
            if kernel32.WaitForSingleObject(ev, timeout_ms) == WAIT_TIMEOUT:
                kernel32.CancelIoEx(self.rh, ctypes.byref(ov))
                return None
            kernel32.GetOverlappedResult(self.rh, ctypes.byref(ov), ctypes.byref(got), False)
            return bytes(buf.raw[:got.value])
        finally:
            kernel32.CloseHandle(ev)

@dataclasses.dataclass
class Reading:
    ok: bool = False
    level: Optional[int] = None
    status: str = "unknown"
    detail: str = ""
    name: str = ""
    ts: float = dataclasses.field(default_factory=time.time)
    present: bool = True

STATUS_TEXT = {"charging": "充电中 ⚡", "discharging": "放电中", "full": "已充满 ⚡", "wired": "有线直连 ⚡", "unknown": "使用中"}

# ===========================================================================
# 达尔优 AE6 V2 专属协议
# ===========================================================================
def dareu_read(ifaces: list[HidIface]) -> Optional[Reading]:
    cands = [d for d in ifaces if d.vid == 0x260D and d.pid in (0x1261, 0x1260, 0x04ED)]
    if not cands:
        return None

    target_ifaces = [d for d in cands if d.usage_page == 0xFF02]
    if not target_ifaces:
        target_ifaces = [d for d in cands if d.usage_page != 0x01] or cands

    for d in target_ifaces:
        dev = None
        try:
            dev = HidDevice(d.path, write=True)

            out_len = d.output_len if d.output_len > 0 else 17
            req = bytearray(out_len)
            req[0] = 0x08
            req[1] = 0x04
            if out_len == 17:
                s = sum(req[:16])
                req[16] = (0x55 - (s & 0xFF)) & 0xFF

            dev.write_output(bytes(req))

            deadline = time.monotonic() + 0.35
            while time.monotonic() < deadline:
                in_len = max(d.input_len, 17)
                rep = dev.read_input(in_len, 80)
                if not rep or len(rep) < 7:
                    continue

                if rep[0] == 0x08 and rep[1] == 0x04:
                    # rep[5] == 2 为充电模式；rep[5] == 1 为放电模式[cite: 15]
                    is_charging = (rep[5] == 2) or (d.pid == 0x1260)
                    bat = rep[6]

                    if 0 <= bat <= 100:
                        if is_charging:
                            status = "full" if bat >= 100 else "charging"
                            detail_str = "已充满 ⚡" if bat >= 100 else f"电量 {bat}% · 充电中 ⚡"
                        else:
                            status = "discharging"
                            detail_str = f"电量 {bat}% · 放电中"

                        return Reading(ok=True, level=bat, status=status,
                                       name="达尔优 AE6 V2", detail=detail_str, present=True)
        except Exception:
            pass
        finally:
            if dev:
                dev.close()

    return Reading(ok=False, name="达尔优 AE6 V2", detail="等待鼠标唤醒 / 未连接", present=True)

# ===========================================================================
# Xbox 360 / Xbox One 无线手柄 XInput 电量读取
# ===========================================================================
class XINPUT_BATTERY_INFORMATION(ctypes.Structure):
    _fields_ = [("BatteryType", ctypes.c_ubyte), ("BatteryLevel", ctypes.c_ubyte)]

# 声明 XInput 函数签名（64 位安全：必须声明 argtypes/restype，否则默认 32 位返回值
# 可能截断 64 位指针，且结构体指针传参可能错位）
if xinput:
    xinput.XInputGetBatteryInformation.restype = wt.DWORD
    xinput.XInputGetBatteryInformation.argtypes = [
        wt.DWORD,                                            # dwUserIndex
        ctypes.c_ubyte,                                      # devType
        ctypes.POINTER(XINPUT_BATTERY_INFORMATION),          # pBatteryInformation
    ]

BATTERY_DEVTYPE_GAMEPAD = 0x00
BATTERY_TYPE_DISCONNECTED = 0x00
BATTERY_TYPE_WIRED = 0x01
BATTERY_TYPE_ALKALINE = 0x02
BATTERY_TYPE_NIMH = 0x03
BATTERY_TYPE_UNKNOWN = 0xFF

# 手柄电量等级映射为百分比估算
BATTERY_LEVEL_EMPTY = 0x00   # 10%
BATTERY_LEVEL_LOW = 0x01     # 30%
BATTERY_LEVEL_MEDIUM = 0x02  # 65%
BATTERY_LEVEL_FULL = 0x03    # 100%

def xbox_read() -> Reading:
    if not xinput:
        return Reading(ok=False, name="Xbox 360 手柄", detail="系统未安装 XInput 组件")

    bat_info = XINPUT_BATTERY_INFORMATION()
    # 检测 Player 0 ~ Player 3
    for user_index in range(4):
        try:
            res = xinput.XInputGetBatteryInformation(
                user_index, BATTERY_DEVTYPE_GAMEPAD, ctypes.byref(bat_info)
            )
            if res == 0:  # ERROR_SUCCESS
                b_type = bat_info.BatteryType
                b_level = bat_info.BatteryLevel

                if b_type == BATTERY_TYPE_DISCONNECTED:
                    continue

                if b_type == BATTERY_TYPE_WIRED:
                    return Reading(ok=True, level=100, status="wired",
                                   name="Xbox 360 手柄", detail="有线连接 · 供电正常 ⚡", present=True)

                level_map = {
                    BATTERY_LEVEL_EMPTY: (10, "极低 · 需充电 ⚡"),
                    BATTERY_LEVEL_LOW: (30, "低电量"),
                    BATTERY_LEVEL_MEDIUM: (65, "良好"),
                    BATTERY_LEVEL_FULL: (100, "充足"),
                }
                pct, desc = level_map.get(b_level, (50, "正常"))
                return Reading(ok=True, level=pct, status="discharging",
                               name="Xbox 360 手柄", detail=f"电量 {desc} (约{pct}%)", present=True)
        except Exception:
            pass

    return Reading(ok=False, name="Xbox 360 手柄", detail="手柄未连接 / 未开机", present=False)

# ===========================================================================
# 图标绘制
# ===========================================================================
class BITMAPINFOHEADER(ctypes.Structure):
    _fields_ = [("biSize", wt.DWORD), ("biWidth", wt.LONG), ("biHeight", wt.LONG),
                ("biPlanes", wt.WORD), ("biBitCount", wt.WORD),
                ("biCompression", wt.DWORD), ("biSizeImage", wt.DWORD),
                ("biXPelsPerMeter", wt.LONG), ("biYPelsPerMeter", wt.LONG),
                ("biClrUsed", wt.DWORD), ("biClrImportant", wt.DWORD)]

class BITMAPINFO(ctypes.Structure):
    _fields_ = [("bmiHeader", BITMAPINFOHEADER), ("bmiColors", wt.DWORD * 3)]

class ICONINFO(ctypes.Structure):
    _fields_ = [("fIcon", wt.BOOL), ("xHotspot", wt.DWORD),
                ("yHotspot", wt.DWORD), ("hbmMask", wt.HBITMAP), ("hbmColor", wt.HBITMAP)]

user32.GetDC.argtypes = [wt.HWND]
user32.GetDC.restype = wt.HDC
user32.ReleaseDC.argtypes = [wt.HWND, wt.HDC]
user32.ReleaseDC.restype = ctypes.c_int
gdi32.CreateCompatibleDC.argtypes = [wt.HDC]
gdi32.CreateCompatibleDC.restype = wt.HDC
gdi32.CreateDIBSection.argtypes = [wt.HDC, ctypes.POINTER(BITMAPINFO), wt.UINT,
                                   ctypes.POINTER(ctypes.c_void_p), wt.HANDLE, wt.DWORD]
gdi32.CreateDIBSection.restype = wt.HBITMAP
gdi32.CreateBitmap.argtypes = [ctypes.c_int, ctypes.c_int, wt.UINT, wt.UINT, ctypes.c_void_p]
gdi32.CreateBitmap.restype = wt.HBITMAP
gdi32.SelectObject.argtypes = [wt.HDC, wt.HGDIOBJ]
gdi32.SelectObject.restype = wt.HGDIOBJ
gdi32.DeleteObject.argtypes = [wt.HGDIOBJ]
gdi32.DeleteObject.restype = wt.BOOL
gdi32.DeleteDC.argtypes = [wt.HDC]
gdi32.DeleteDC.restype = wt.BOOL
user32.CreateIconIndirect.argtypes = [ctypes.POINTER(ICONINFO)]
user32.CreateIconIndirect.restype = wt.HICON
user32.DestroyIcon.argtypes = [wt.HICON]
user32.DestroyIcon.restype = wt.BOOL

COLORREF = lambda r, g, b: (r & 0xFF) | ((g & 0xFF) << 8) | ((b & 0xFF) << 16)

class _LogBrush(ctypes.Structure):
    _fields_ = [("lbStyle", wt.UINT), ("lbColor", wt.COLORREF), ("lbHatch", ctypes.c_void_p)]

# 补充 GDI / User32 函数签名（64 位安全）
gdi32.CreateSolidBrush.restype = wt.HBRUSH
gdi32.CreateSolidBrush.argtypes = [wt.COLORREF]
gdi32.ExtCreatePen.restype = wt.HPEN
gdi32.ExtCreatePen.argtypes = [wt.DWORD, wt.DWORD, ctypes.POINTER(_LogBrush), wt.DWORD, ctypes.POINTER(wt.DWORD)]
gdi32.GetStockObject.restype = wt.HGDIOBJ
gdi32.GetStockObject.argtypes = [ctypes.c_int]
gdi32.Ellipse.restype = wt.BOOL
gdi32.Ellipse.argtypes = [wt.HDC, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_int]
gdi32.Arc.restype = wt.BOOL
gdi32.Arc.argtypes = [wt.HDC, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_int,
                      ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_int]
gdi32.GdiFlush.restype = wt.BOOL
gdi32.GdiFlush.argtypes = []
gdi32.CreateRoundRectRgn.restype = wt.HRGN
gdi32.CreateRoundRectRgn.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_int]
user32.FillRect.restype = ctypes.c_int
user32.FillRect.argtypes = [wt.HDC, ctypes.POINTER(wt.RECT), wt.HBRUSH]
user32.SetWindowRgn.restype = ctypes.c_int
user32.SetWindowRgn.argtypes = [wt.HWND, wt.HRGN, wt.BOOL]

def _ring_pen(rgb, width: int) -> int:
    lb = _LogBrush(lbStyle=0, lbColor=COLORREF(*rgb), lbHatch=0)
    return gdi32.ExtCreatePen(0x00010000 | 0x00000200, width, ctypes.byref(lb), 0, None)

def make_battery_icon(level: Optional[int], light: bool, charging: bool = False, size: int = 32) -> int:
    SS = 4
    big = size * SS
    hdc_screen = user32.GetDC(None)
    memdc = gdi32.CreateCompatibleDC(hdc_screen)

    bmi = BITMAPINFO()
    bmi.bmiHeader.biSize = ctypes.sizeof(BITMAPINFOHEADER)
    bmi.bmiHeader.biWidth = big
    bmi.bmiHeader.biHeight = -big
    bmi.bmiHeader.biPlanes = 1
    bmi.bmiHeader.biBitCount = 32
    bits = ctypes.c_void_p()
    hbm_big = gdi32.CreateDIBSection(memdc, ctypes.byref(bmi), 0, ctypes.byref(bits), None, 0)
    old = gdi32.SelectObject(memdc, hbm_big)
    try:
        bg = gdi32.CreateSolidBrush(COLORREF(255, 0, 255))
        rect = wt.RECT(0, 0, big, big)
        user32.FillRect(memdc, ctypes.byref(rect), bg)
        gdi32.DeleteObject(bg)

        s = big / 32.0
        cx = cy = big / 2.0
        radius = 12.0 * s
        thick = max(2, int(round(4 * s)))

        track = (175, 175, 175) if level is None else (95, 98, 105)
        pen_track = _ring_pen(track, thick)
        gdi32.SelectObject(memdc, pen_track)
        gdi32.SelectObject(memdc, gdi32.GetStockObject(5))
        gdi32.Ellipse(memdc, int(cx - radius), int(cy - radius), int(cx + radius), int(cy + radius))

        if level is not None and level >= 0:
            if charging:
                r, g, b = 34, 197, 94
            elif level >= 60:
                r, g, b = 34, 197, 94
            elif level >= 30:
                r, g, b = 245, 158, 11
            else:
                r, g, b = 239, 68, 68
            pen_arc = _ring_pen((r, g, b), thick)
            gdi32.SelectObject(memdc, pen_arc)
            if level >= 100:
                gdi32.Ellipse(memdc, int(cx - radius), int(cy - radius), int(cx + radius), int(cy + radius))
            else:
                theta = 2.0 * math.pi * max(2, min(100, level)) / 100.0
                ex = cx + radius * math.cos(-math.pi / 2 + theta)
                ey = cy + radius * math.sin(-math.pi / 2 + theta)
                gdi32.Arc(memdc, int(cx - radius), int(cy - radius), int(cx + radius), int(cy + radius),
                          int(round(ex)), int(round(ey)), int(cx), int(cy - radius))
            gdi32.DeleteObject(pen_arc)

        gdi32.SelectObject(memdc, old)
        gdi32.GdiFlush()
        raw = ctypes.string_at(bits, big * big * 4)

        small = bytearray(size * size * 4)
        for y in range(size):
            for x in range(size):
                cnt, rs, gs, bs = 0, 0, 0, 0
                for yy in range(y * SS, (y + 1) * SS):
                    base = yy * big * 4 + x * SS * 4
                    for xx in range(SS):
                        i = base + xx * 4
                        b_, g_, r_ = raw[i], raw[i + 1], raw[i + 2]
                        if (r_, g_, b_) != (255, 0, 255):
                            cnt += 1
                            rs += r_
                            gs += g_
                            bs += b_
                o = (y * size + x) * 4
                if cnt:
                    small[o] = bs // cnt
                    small[o + 1] = gs // cnt
                    small[o + 2] = rs // cnt
                    small[o + 3] = cnt * 255 // (SS * SS)

        bmi2 = BITMAPINFO()
        bmi2.bmiHeader.biSize = ctypes.sizeof(BITMAPINFOHEADER)
        bmi2.bmiHeader.biWidth = size
        bmi2.bmiHeader.biHeight = -size
        bmi2.bmiHeader.biPlanes = 1
        bmi2.bmiHeader.biBitCount = 32
        bits2 = ctypes.c_void_p()
        hbm_small = gdi32.CreateDIBSection(memdc, ctypes.byref(bmi2), 0, ctypes.byref(bits2), None, 0)
        ctypes.memmove(bits2, bytes(small), len(small))

        mask_bytes = bytes([0] * ((size + 31) // 32 * 4 * size))
        mask = gdi32.CreateBitmap(size, size, 1, 1, mask_bytes)
        ii = ICONINFO(fIcon=1, xHotspot=0, yHotspot=0, hbmMask=mask, hbmColor=hbm_small)
        icon = user32.CreateIconIndirect(ctypes.byref(ii))
        gdi32.DeleteObject(mask)
        gdi32.DeleteObject(hbm_small)
        return icon
    finally:
        gdi32.DeleteObject(hbm_big)
        gdi32.DeleteDC(memdc)
        user32.ReleaseDC(None, hdc_screen)

# ===========================================================================
# 交互式管理面板（双设备卡片）
# ===========================================================================
FONT = "Microsoft YaHei UI"

def _rgb(c):
    return "#%02x%02x%02x" % c

def round_rect_points(x1, y1, x2, y2, r, steps=10):
    pts = []
    for cx, cy, a0 in ((x2 - r, y1 + r, -90), (x2 - r, y2 - r, 0), (x1 + r, y2 - r, 90), (x1 + r, y1 + r, 180)):
        for i in range(steps + 1):
            a = (a0 + 90 * i / steps) * 3.141592653589793 / 180
            pts.append((cx + r * math.cos(a), cy + r * math.sin(a)))
    return pts

def draw_round_rect(cv: "tk.Canvas", x1, y1, x2, y2, r, **kw):
    pts = round_rect_points(x1, y1, x2, y2, r)
    flat = [v for p in pts for v in p]
    return cv.create_polygon(flat, smooth=False, **kw)

def set_round_region(hwnd: int, w: int, h: int, radius: int = 24) -> None:
    try:
        rgn = gdi32.CreateRoundRectRgn(0, 0, w + 1, h + 1, radius, radius)
        user32.SetWindowRgn(hwnd, rgn, True)
    except Exception:
        pass

class Panel:
    LW, LH = 560, 420

    def _f(self, size, bold=False):
        return (FONT, -int(round(size * self.S)), "bold" if bold else "normal")

    def __init__(self, app: "TrayApp"):
        self.app = app
        self.S = max(1.0, UI_SCALE)
        self.W = int(self.LW * self.S)
        self.H = int(self.LH * self.S)
        self.root = tk.Tk()
        self.root.title("外设电量监控")
        self.root.overrideredirect(True)
        self.root.configure(bg="#1B1B1C")
        self.cv = tk.Canvas(self.root, width=self.W, height=self.H, highlightthickness=0, bd=0, bg="#1B1B1C")
        self.cv.pack(fill="both", expand=True)
        self.cv.bind("<Button-1>", self._on_press)
        self.cv.bind("<B1-Motion>", self._on_drag)
        self.cv.bind("<ButtonRelease-1>", lambda e: setattr(self, "_drag", None))
        self.root.bind("<Escape>", lambda e: self.minimize_to_tray())
        self._drag = None
        self.root.update_idletasks()
        set_round_region(self.root.winfo_id(), self.W, self.H, int(28 * self.S))
        self._center()

    def _center(self):
        sw = self.root.winfo_screenwidth()
        sh = self.root.winfo_screenheight()
        x = max(0, (sw - self.W) // 2)
        y = max(0, (sh - self.H) // 2 - 40)
        self.root.geometry(f"{self.W}x{self.H}+{x}+{y}")

    def _on_press(self, e):
        if e.y >= 64 * self.S:
            return
        tags = self.cv.gettags("current")
        if any(t.startswith("btn_") for t in tags):
            return
        self._drag = (e.x_root, e.y_root, self.root.winfo_x(), self.root.winfo_y())

    def _on_drag(self, e):
        if not self._drag:
            return
        x0, y0, wx, wy = self._drag
        self.root.geometry(f"+{wx + e.x_root - x0}+{wy + e.y_root - y0}")

    def show(self):
        self.render()
        self.root.deiconify()
        self.root.lift()
        self.root.attributes("-topmost", True)
        self.root.after(100, lambda: self.root.attributes("-topmost", False))

    def hide(self):
        self.root.withdraw()

    def minimize_to_tray(self):
        self.hide()

    DSH_BG = (27, 27, 28)
    DSH_CARD = (33, 33, 35)
    DSH_BORDER = (48, 49, 54)
    DSH_TEXT = (245, 245, 247)
    DSH_SUB = (160, 163, 168)
    DSH_MUTED = (120, 123, 128)
    DSH_BTN = (67, 69, 74)
    DSH_TRACK = (46, 47, 51)

    def _draw_card(self, cv, x1, y1, x2, y2, r: Reading):
        draw_round_rect(cv, x1, y1, x2, y2, 12, fill=_rgb(self.DSH_CARD), outline=_rgb(self.DSH_BORDER), width=1)
        cv.create_text(x1 + 18, y1 + 22, text=r.name, anchor="w", fill=_rgb(self.DSH_TEXT), font=self._f(13, True))
        cv.create_text(x1 + 18, y1 + 46, text=r.detail, anchor="w", fill=_rgb(self.DSH_SUB), font=self._f(9))

        lv = r.level if r.ok else None
        if r.ok and lv is not None:
            col = (34, 197, 94) if lv > 20 else (239, 68, 68)
            cv.create_text(x2 - 46, y1 + 40, text=str(lv), anchor="e", fill=_rgb(col), font=self._f(30, True))
            cv.create_text(x2 - 24, y1 + 40, text="%", anchor="e", fill=_rgb(self.DSH_SUB), font=self._f(12, True))
        else:
            cv.create_text(x2 - 24, y1 + 40, text="未连接", anchor="e", fill=_rgb(self.DSH_MUTED), font=self._f(14, True))

        bx1, bx2 = x1 + 18, x2 - 18
        by = y2 - 24
        draw_round_rect(cv, bx1, by, bx2, by + 6, 3, fill=_rgb(self.DSH_TRACK), outline="")
        if lv is not None and lv > 0:
            w = int((bx2 - bx1) * (lv / 100.0))
            col = (34, 197, 94) if lv > 20 else (239, 68, 68)
            draw_round_rect(cv, bx1, by, bx1 + max(6, w), by + 6, 3, fill=_rgb(col), outline="")

    def render(self):
        cv = self.cv
        cv.delete("all")
        W, H = self.LW, self.LH
        cv.create_rectangle(0, 0, W, H, fill=_rgb(self.DSH_BG), outline="")

        snap = self.app.snapshot()
        r_mouse = snap.get("mouse") or Reading(ok=False, name="达尔优 AE6 V2")
        r_xbox = snap.get("xbox") or Reading(ok=False, name="Xbox 360 手柄")

        # 卡片 1: 达尔优鼠标
        self._draw_card(cv, 24, 80, W - 24, 196, r_mouse)

        # 卡片 2: Xbox 手柄
        self._draw_card(cv, 24, 212, W - 24, 328, r_xbox)

        # 顶栏
        cv.create_text(30, 34, text="外设电量", anchor="w", fill=_rgb(self.DSH_TEXT), font=self._f(16, True))
        cv.create_text(30, 56, text="无线电量实时监控 · 鼠标与手柄", anchor="w", fill=_rgb(self.DSH_SUB), font=self._f(9))

        cv.create_text(W - 30, 30, text="✕", fill=_rgb(self.DSH_SUB), font=self._f(11), tags=("btn_close",))
        cv.tag_bind("btn_close", "<Button-1>", lambda e: self.minimize_to_tray())
        cv.tag_bind("btn_close", "<Enter>", lambda e: cv.configure(cursor="hand2"))
        cv.tag_bind("btn_close", "<Leave>", lambda e: cv.configure(cursor=""))

        # 底栏
        t_mouse = r_mouse.ts if r_mouse.ts else time.time()
        stamp = time.strftime("%H:%M:%S", time.localtime(t_mouse))
        cv.create_text(24, 385, text=f"上次更新: {stamp}", anchor="w", fill=_rgb(self.DSH_MUTED), font=self._f(9), tags=("btn_ref",))
        cv.tag_bind("btn_ref", "<Button-1>", lambda e: self.app.refresh())

        draw_round_rect(cv, 365, 372, 460, 398, 6, fill=_rgb(self.DSH_BTN), outline="")
        cv.create_text(412, 385, text="最小化到托盘", fill=_rgb(self.DSH_TEXT), font=self._f(9), tags=("btn_min",))
        cv.tag_bind("btn_min", "<Button-1>", lambda e: self.minimize_to_tray())

        draw_round_rect(cv, 475, 372, 536, 398, 6, fill=_rgb(self.DSH_BTN), outline="")
        cv.create_text(505, 385, text="退出", fill=_rgb(self.DSH_TEXT), font=self._f(9), tags=("btn_quit",))
        cv.tag_bind("btn_quit", "<Button-1>", lambda e: self.app.quit())

        cv.scale("all", 0, 0, self.S, self.S)

# ===========================================================================
# 托盘调度与消息循环
# ===========================================================================
WM_APP = 0x8000
WM_TRAYICON = WM_APP + 1
WM_APP_SHOW = WM_APP + 3

WNDPROC = ctypes.WINFUNCTYPE(ctypes.c_ssize_t, wt.HWND, wt.UINT, wt.WPARAM, wt.LPARAM)

class WNDCLASSW(ctypes.Structure):
    _fields_ = [
        ("style", wt.UINT), ("lpfnWndProc", WNDPROC),
        ("cbClsExtra", ctypes.c_int), ("cbWndExtra", ctypes.c_int),
        ("hInstance", wt.HINSTANCE), ("hIcon", wt.HICON),
        ("hCursor", wt.HANDLE), ("hbrBackground", wt.HBRUSH),
        ("lpszMenuName", wt.LPCWSTR), ("lpszClassName", wt.LPCWSTR),
    ]

class NOTIFYICONDATAW(ctypes.Structure):
    _fields_ = [
        ("cbSize", wt.DWORD), ("hWnd", wt.HWND), ("uID", wt.UINT),
        ("uFlags", wt.UINT), ("uCallbackMessage", wt.UINT),
        ("hIcon", wt.HICON), ("szTip", wt.WCHAR * 128),
        ("dwState", wt.DWORD), ("dwStateMask", wt.DWORD),
        ("szInfo", wt.WCHAR * 256), ("uVersion", wt.UINT),
        ("szInfoTitle", wt.WCHAR * 64), ("dwInfoFlags", wt.DWORD),
        ("guidItem", GUID), ("hBalloonIcon", wt.HICON),
    ]

user32.DefWindowProcW.restype = ctypes.c_ssize_t
user32.DefWindowProcW.argtypes = [wt.HWND, wt.UINT, wt.WPARAM, wt.LPARAM]
user32.RegisterClassW.argtypes = [ctypes.POINTER(WNDCLASSW)]
user32.RegisterClassW.restype = wt.ATOM
user32.CreateWindowExW.restype = wt.HWND
user32.CreateWindowExW.argtypes = [
    wt.DWORD, wt.LPCWSTR, wt.LPCWSTR, wt.DWORD,
    ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_int,
    wt.HWND, wt.HMENU, wt.HINSTANCE, ctypes.c_void_p
]
user32.DestroyWindow.argtypes = [wt.HWND]
user32.DestroyWindow.restype = wt.BOOL
user32.GetMessageW.argtypes = [ctypes.POINTER(wt.MSG), wt.HWND, wt.UINT, wt.UINT]
user32.GetMessageW.restype = wt.BOOL
user32.TranslateMessage.argtypes = [ctypes.POINTER(wt.MSG)]
user32.DispatchMessageW.argtypes = [ctypes.POINTER(wt.MSG)]
user32.PostMessageW.argtypes = [wt.HWND, wt.UINT, wt.WPARAM, wt.LPARAM]
user32.PostMessageW.restype = wt.BOOL
user32.FindWindowW.argtypes = [wt.LPCWSTR, wt.LPCWSTR]
user32.FindWindowW.restype = wt.HWND
shell32.Shell_NotifyIconW.argtypes = [wt.DWORD, ctypes.POINTER(NOTIFYICONDATAW)]
shell32.Shell_NotifyIconW.restype = wt.BOOL

class SystemTray:
    CLASS_NAME = "BatteryTrayDual_Native"

    def __init__(self, on_click_callback):
        self.on_click_callback = on_click_callback
        self.hwnd = 0
        self.cur_icon = 0
        self.nid = None
        self._wndproc = WNDPROC(self._proc)
        self._ready_event = threading.Event()
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        self._ready_event.wait(timeout=2.0)

    def _run(self):
        hinst = kernel32.GetModuleHandleW(None)
        wc = WNDCLASSW()
        wc.lpfnWndProc = self._wndproc
        wc.hInstance = hinst
        wc.lpszClassName = self.CLASS_NAME
        user32.RegisterClassW(ctypes.byref(wc))

        self.hwnd = user32.CreateWindowExW(0, self.CLASS_NAME, "DualBatteryTray", 0, 0, 0, 0, 0, None, None, hinst, None)
        if not self.hwnd:
            self._ready_event.set()
            return

        self.nid = NOTIFYICONDATAW()
        self.nid.cbSize = ctypes.sizeof(NOTIFYICONDATAW)
        self.nid.hWnd = self.hwnd
        self.nid.uID = 1
        self.nid.uFlags = 1 | 2 | 4
        self.nid.uCallbackMessage = WM_TRAYICON
        self.cur_icon = make_battery_icon(None, False, False)
        self.nid.hIcon = self.cur_icon
        self.nid.szTip = "外设电量监控: 启动中..."
        shell32.Shell_NotifyIconW(0, ctypes.byref(self.nid))

        self._ready_event.set()

        msg = wt.MSG()
        while user32.GetMessageW(ctypes.byref(msg), None, 0, 0) > 0:
            user32.TranslateMessage(ctypes.byref(msg))
            user32.DispatchMessageW(ctypes.byref(msg))

    def update(self, level: Optional[int], charging: bool, tip_text: str):
        if not self.hwnd or not self.nid:
            return
        new_icon = make_battery_icon(level, False, charging)
        self.nid.hIcon = new_icon
        self.nid.szTip = tip_text[:127]
        shell32.Shell_NotifyIconW(1, ctypes.byref(self.nid))
        if self.cur_icon:
            user32.DestroyIcon(self.cur_icon)
        self.cur_icon = new_icon

    def destroy(self):
        if self.hwnd and self.nid:
            shell32.Shell_NotifyIconW(2, ctypes.byref(self.nid))
        if self.cur_icon:
            user32.DestroyIcon(self.cur_icon)
        if self.hwnd:
            user32.PostMessageW(self.hwnd, 0x0010, 0, 0)

    def _proc(self, hwnd, msg, wparam, lparam):
        if msg == WM_TRAYICON:
            cmd = lparam & 0xFFFF
            if cmd in (0x0202, 0x0203, 0x0205):
                self.on_click_callback()
            return 0
        if msg == WM_APP_SHOW:
            self.on_click_callback()
            return 0
        if msg in (0x0010, 0x0002):
            user32.PostQuitMessage(0)
            return 0
        return user32.DefWindowProcW(hwnd, msg, wparam, lparam)

# ===========================================================================
# 应用程序核心编排
# ===========================================================================
class TrayApp:
    def __init__(self):
        self._readings: dict[str, Reading] = {}
        self._lock = threading.Lock()
        self.panel: Optional[Panel] = None
        self.tray: Optional[SystemTray] = None
        self._running = True

    def snapshot(self) -> dict[str, Reading]:
        with self._lock:
            return dict(self._readings)

    def refresh(self):
        threading.Thread(target=self._worker, daemon=True).start()

    def _worker(self):
        try:
            ifaces = enumerate_hid()
            r_mouse = dareu_read(ifaces)
        except Exception:
            r_mouse = Reading(ok=False, name="达尔优 AE6 V2", detail="读取失败")

        r_xbox = xbox_read()

        with self._lock:
            self._readings = {"mouse": r_mouse, "xbox": r_xbox}

        # 优先以鼠标为主托盘图标，若鼠标未连接则展示手柄电量
        main_r = r_mouse if r_mouse.ok else (r_xbox if r_xbox.ok else r_mouse)
        lvl = main_r.level if (main_r and main_r.ok) else None
        charging = bool(main_r and main_r.status in ("charging", "full", "wired"))

        tip_lines = [
            f"鼠标: {r_mouse.level}%" if r_mouse.ok else "鼠标: 未连接",
            f"手柄: {r_xbox.level}%" if r_xbox.ok else "手柄: 未连接",
        ]
        tip = " | ".join(tip_lines)

        if self.tray:
            self.tray.update(lvl, charging, tip)

        if self.panel and self.panel.root.winfo_viewable():
            self.panel.root.after(0, self.panel.render)

    def request_show(self):
        if self.panel:
            self.panel.root.after(0, self.panel.show)

    def quit(self):
        self._running = False
        if self.tray:
            self.tray.destroy()
        if self.panel:
            try:
                self.panel.root.destroy()
            except Exception:
                pass
        os._exit(0)

    def run(self):
        self.panel = Panel(self)
        self.tray = SystemTray(on_click_callback=self.request_show)

        self.refresh()
        self.panel.show()

        def auto_loop():
            if not self._running:
                return
            self.refresh()
            self.panel.root.after(3000, auto_loop)

        self.panel.root.after(3000, auto_loop)
        self.panel.root.mainloop()

def main():
    user32.SetProcessDPIAware()

    mutex = kernel32.CreateMutexW(None, False, "Local\\BatteryTrayDual_NativeMutex")
    if ctypes.get_last_error() == 183 and mutex:
        hwnd = user32.FindWindowW("BatteryTrayDual_Native", None)
        if hwnd:
            user32.PostMessageW(hwnd, WM_APP_SHOW, 0, 0)
        return

    app = TrayApp()
    app.run()

if __name__ == "__main__":
    main()
