达尔优 AE6 V2 外设电量监控
项目说明
本项目是纯 Python 项目，使用标准库 ctypes + tkinter，零第三方依赖。
入口文件为 my_devices.py（历史遗留名 main.go，内容实为 Python 脚本）。
本地运行
python my_devices.py
构建为 Windows EXE
pip install pyinstaller
pyinstaller --noconfirm --onefile --windowed --name "DareuAE6V2Tray" --icon "app.ico" my_devices.py
生成的 exe 在 dist/DareuAE6V2Tray.exe。
注意事项
- 不要使用 go build：本项目不是 Go 项目，go.mod 仅保留兼容，不应参与构建
- 如仓库中同时存在 main.go 和 my_devices.py，workflow 会优先使用 my_devices.py
