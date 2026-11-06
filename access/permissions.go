package access

type Permission string

const (
	ViewChatID          Permission = "view_chat_id"
	CreateOneShot       Permission = "create_one_shot"
	ManageQueueMessages Permission = "manage_queue_messages"
	EditSchedule        Permission = "edit_schedule"
	ManageChats         Permission = "manage_chats"
	ChangeWeek          Permission = "change_week"
	MarkOthers          Permission = "mark_others"
	RemoveParticipants  Permission = "remove_participants"
)

func AllPermissions() []Permission {
	return []Permission{ViewChatID, CreateOneShot, ManageQueueMessages, EditSchedule, ManageChats, ChangeWeek, MarkOthers, RemoveParticipants}
}

func validPermission(permission Permission) bool {
	switch permission {
	case ViewChatID, CreateOneShot, ManageQueueMessages, EditSchedule, ManageChats, ChangeWeek, MarkOthers, RemoveParticipants:
		return true
	default:
		return false
	}
}
