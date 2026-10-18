package model

type Process struct {
	PID         uint32
	ExePath     string
	Platform    string
	Version     int
	FullVersion string
	Status      string
	DataDir     string
	AccountName string
}

const PlatformDarwin = "darwin"

const (
	StatusInit    = ""
	StatusOffline = "offline"
	StatusOnline  = "online"
)
