package utils

type QueueStatus int

const (
    QueuePending QueueStatus = iota
    QueueOpen
    QueueClosed
)