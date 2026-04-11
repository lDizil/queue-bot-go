package middleware

import "github.com/go-telegram/bot"

func Chain(h bot.HandlerFunc, middlewares ...func(bot.HandlerFunc) bot.HandlerFunc) bot.HandlerFunc {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}

	return h
}

