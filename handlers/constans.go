package handlers

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
    "чётная":   "odd",
    "нечётная": "even",
}

var weekTypeEnToRu = map[string]string{
    "odd": "чётная",
    "even":  "нечётная",
}