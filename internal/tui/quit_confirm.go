package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const quitArmDuration = 2 * time.Second

type quitArmExpiredMsg struct {
	id uint64
}

type quitArm struct {
	armed   bool
	id      uint64
	message string
	tick    func(uint64) tea.Cmd
}

func newQuitArm() quitArm {
	return quitArm{tick: quitArmTick}
}

func quitArmTick(id uint64) tea.Cmd {
	return tea.Tick(quitArmDuration, func(time.Time) tea.Msg {
		return quitArmExpiredMsg{id: id}
	})
}

func (a *quitArm) arm(message string) tea.Cmd {
	a.id++
	a.armed = true
	a.message = message
	return a.tick(a.id)
}

func (a *quitArm) disarm() {
	a.armed = false
	a.message = ""
}

func (a *quitArm) expire(msg quitArmExpiredMsg) bool {
	if !a.armed || msg.id != a.id {
		return false
	}
	a.disarm()
	return true
}
