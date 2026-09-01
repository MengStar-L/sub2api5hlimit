<script setup lang="ts">
import { Inbox } from 'lucide-vue-next'
defineProps<{ title: string; description: string }>()
</script>

<template>
  <div class="empty-state">
    <span class="empty-icon"><Inbox :size="18" /></span>
    <h3>{{ title }}</h3>
    <p>{{ description }}</p>
    <slot />
  </div>
</template>
