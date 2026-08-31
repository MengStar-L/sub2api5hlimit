import { reactive } from 'vue'

export interface ToastItem {
  id: number
  type: 'success' | 'error' | 'info'
  title: string
  message?: string
}

const items = reactive<ToastItem[]>([])
let nextId = 1

function show(type: ToastItem['type'], title: string, message?: string) {
  const item = { id: nextId++, type, title, message }
  items.push(item)
  window.setTimeout(() => dismiss(item.id), 4200)
}

function dismiss(id: number) {
  const index = items.findIndex(item => item.id === id)
  if (index >= 0) items.splice(index, 1)
}

export const toast = {
  items,
  show,
  dismiss,
  success: (title: string, message?: string) => show('success', title, message),
  error: (title: string, message?: string) => show('error', title, message),
  info: (title: string, message?: string) => show('info', title, message),
}
