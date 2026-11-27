package tray

import (
	"github.com/getlantern/systray"
	"time"
)

type Actions struct {
	Open   func()
	Start  func()
	Stop   func()
	Quit   func()
	Status func() string
	Exit   func()
}

func Run(actions Actions) {
	systray.Run(func() {
		systray.SetTitle("remote-codex")
		systray.SetTooltip("remote-codex")
		status := systray.AddMenuItem(actions.Status(), "")
		status.Disable()
		open := systray.AddMenuItem("打开控制面板", "")
		start := systray.AddMenuItem("连接 ChatGPT", "")
		stop := systray.AddMenuItem("停止宿主连接", "")
		systray.AddSeparator()
		quit := systray.AddMenuItem("退出网关", "")
		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					status.SetTitle(actions.Status())
				case <-open.ClickedCh:
					actions.Open()
				case <-start.ClickedCh:
					actions.Start()
				case <-stop.ClickedCh:
					actions.Stop()
				case <-quit.ClickedCh:
					actions.Quit()
					systray.Quit()
					return
				}
			}
		}()
	}, actions.Exit)
}
func Quit() { systray.Quit() }
