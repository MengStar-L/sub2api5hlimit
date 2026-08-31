<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'

const props = defineProps<{ open: boolean; title: string; description?: string }>()
const emit = defineEmits<{ close: [] }>()
const panel = ref<HTMLElement | null>(null)
let returnFocus: HTMLElement | null = null
let previousOverflow = ''

const focusableSelector = 'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])'

function handleKeydown(event: KeyboardEvent) {
  if (!props.open) return
  if (event.key === 'Escape') {
    event.preventDefault()
    emit('close')
    return
  }
  if (event.key !== 'Tab' || !panel.value) return
  const focusable = Array.from(panel.value.querySelectorAll<HTMLElement>(focusableSelector))
  if (focusable.length === 0) {
    event.preventDefault()
    panel.value.focus()
    return
  }
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (!panel.value.contains(document.activeElement)) {
    event.preventDefault()
    ;(event.shiftKey ? last : first).focus()
    return
  }
  if (event.shiftKey && (document.activeElement === first || document.activeElement === panel.value)) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

watch(() => props.open, async open => {
  if (open) {
    returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
    previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    document.addEventListener('keydown', handleKeydown)
    await nextTick()
    panel.value?.querySelector<HTMLElement>(focusableSelector)?.focus()
    return
  }
  document.removeEventListener('keydown', handleKeydown)
  document.body.style.overflow = previousOverflow
  returnFocus?.focus()
  returnFocus = null
}, { immediate: true })

onBeforeUnmount(() => {
  document.removeEventListener('keydown', handleKeydown)
  document.body.style.overflow = previousOverflow
  returnFocus?.focus()
})
</script>

<template>
  <Teleport to="body">
    <Transition name="drawer">
      <div v-if="open" class="drawer-layer" @click.self="$emit('close')">
        <aside ref="panel" class="drawer-panel" role="dialog" aria-modal="true" :aria-label="title" tabindex="-1">
          <header class="drawer-header">
            <div><h2>{{ title }}</h2><p v-if="description">{{ description }}</p></div>
            <button class="icon-button" type="button" aria-label="关闭" @click="$emit('close')"><X :size="19" /></button>
          </header>
          <div class="drawer-body"><slot /></div>
          <footer v-if="$slots.footer" class="drawer-footer"><slot name="footer" /></footer>
        </aside>
      </div>
    </Transition>
  </Teleport>
</template>
