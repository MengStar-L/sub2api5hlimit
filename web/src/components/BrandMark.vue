<script setup lang="ts">
defineProps<{ compact?: boolean }>()
</script>

<template>
  <div class="brand" :class="{ compact }">
    <span class="brand-mark" aria-hidden="true">
      <span></span><span></span><span></span>
    </span>
    <span v-if="!compact" class="brand-copy">
      <strong>Sub2API</strong>
      <small>配额中心</small>
    </span>
  </div>
</template>
