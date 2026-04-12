package handlers

import (
	"fmt"

	"github.com/go-telegram/bot/models"
)

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
	"even": "четная",
	"odd":  "нечетная",
}

var backBtnMainMenu = models.InlineKeyboardButton{
	Text:         "Назад",
	CallbackData: "Back:mainmenu",
}

func GetBackBtnEditSch(scheduleID int) models.InlineKeyboardButton {
	var BackBtnEditSch = models.InlineKeyboardButton{
		Text:         "Назад",
		CallbackData: fmt.Sprintf("Back:editschmenu:%d", scheduleID),
	}

	return BackBtnEditSch
}

func GetBackBtnEditTime(scheduleID int) models.InlineKeyboardButton {
	var BackBtnEditSch = models.InlineKeyboardButton{
		Text:         "Назад",
		CallbackData: fmt.Sprintf("Back:editschtime:%d", scheduleID),
	}

	return BackBtnEditSch
}

var dayEnToRuShort = map[string]string{
    "monday":    "пн",
    "tuesday":   "вт",
    "wednesday": "ср",
    "thursday":  "чт",
    "friday":    "пт",
    "saturday":  "сб",
    "sunday":    "вс",
}

var weekTypeEnToRuShort = map[string]string{
    "even": "чет",
    "odd":  "нечет",
}