package usecase

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"
)

type ServerDatabase interface {
	Start() error
	SetError(message string)
}

type ServerHTTP interface {
	ListenAndServe() error
}

type RuntimeLifecycle interface {
	Stop() error
}

// Server owns headless database bootstrap, retry and signal-driven shutdown.
type Server struct {
	db        ServerDatabase
	http      ServerHTTP
	lifecycle RuntimeLifecycle
}

func NewServer(db ServerDatabase, http ServerHTTP, lifecycle RuntimeLifecycle) *Server {
	return &Server{db: db, http: http, lifecycle: lifecycle}
}

func (u *Server) Run() error {
	go u.startDatabase()

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		<-signalContext.Done()
		log.Info().Msg("收到退出信号：停止运行时服务")
		if err := u.lifecycle.Stop(); err != nil {
			log.Error().Err(err).Msg("运行时服务退出失败")
		}
	}()
	return u.http.ListenAndServe()
}

func (u *Server) startDatabase() {
	if err := u.db.Start(); err != nil {
		log.Info().Err(err).Msg("direct database start failed")
		u.db.SetError(err.Error())
	}
}
