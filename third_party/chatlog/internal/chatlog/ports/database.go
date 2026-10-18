package ports

type DatabaseState uint8

const (
	DatabaseStateInit DatabaseState = iota
	DatabaseStateOpening
	DatabaseStateReady
	DatabaseStateError
)
