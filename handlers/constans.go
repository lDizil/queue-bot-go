package handlers

import "github.com/go-telegram/bot/models"

var dayRuToEn = map[string]string{
	"понедельник": "monday",
	"вторник":     "tuesday",
	"среда":       "wednesday",
	"четверг":     "thursday",
	"пятница":     "friday",
	"суббота":     "saturday",
	"воскресенье": "sunday",
}

var dayEnToRu = map[string]string{
	"monday":    "понедельник",
	"tuesday":   "вторник",
	"wednesday": "среда",
	"thursday":  "четверг",
	"friday":    "пятница",
	"saturday":  "суббота",
	"sunday":    "воскресенье",
}

var weekTypeRuToEn = map[string]string{
    "чётная":   "even",
    "четная":   "even",
    "нечётная": "odd",
    "нечетная": "odd",
}

var weekTypeEnToRu = map[string]string{
	"even":  "четная",
	"odd": "нечетная",
}

var BackBtn = models.InlineKeyboardButton{
	Text:         "Назад",
	CallbackData: "Back",
}
