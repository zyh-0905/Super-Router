<script setup>
// 线性图标集（SF Symbols 风格：1.7px 圆头描边）
defineProps({
  name: { type: String, required: true },
  size: { type: [Number, String], default: 18 },
})

const icons = {
  grid: '<rect x="3" y="3" width="7.5" height="7.5" rx="2"/><rect x="13.5" y="3" width="7.5" height="7.5" rx="2"/><rect x="3" y="13.5" width="7.5" height="7.5" rx="2"/><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="2"/>',
  server: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><circle cx="7" cy="7.5" r="1"/><circle cx="7" cy="16.5" r="1"/>',
  beaker: '<path d="M9 3h6M10 3v6.2L4.8 17.6A2 2 0 0 0 6.6 20h10.8a2 2 0 0 0 1.8-2.4L14 9.2V3"/><path d="M7.5 14h9"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13"/><circle cx="4" cy="6" r="1.2"/><circle cx="4" cy="12" r="1.2"/><circle cx="4" cy="18" r="1.2"/>',
  bolt: '<path d="M13 2 4.5 13.5H11L10 22l8.5-11.5H12L13 2z"/>',
  gear: '<circle cx="12" cy="12" r="3.2"/><path d="M12 2.8v2.4M12 18.8v2.4M2.8 12h2.4M18.8 12h2.4M5.5 5.5l1.7 1.7M16.8 16.8l1.7 1.7M18.5 5.5l-1.7 1.7M7.2 16.8l-1.7 1.7"/>',
  sun: '<circle cx="12" cy="12" r="4.2"/><path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M18.7 5.3l-1.4 1.4M6.7 17.3l-1.4 1.4"/>',
  moon: '<path d="M20.5 14.5A8.5 8.5 0 0 1 9.5 3.5a8.5 8.5 0 1 0 11 11z"/>',
  auto: '<rect x="2.5" y="4" width="19" height="12" rx="2.5"/><path d="M7 20h10M12 16v4"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  x: '<path d="M6 6l12 12M18 6L6 18"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="M20.5 20.5 16 16"/>',
  refresh: '<path d="M20 12a8 8 0 1 1-2.34-5.66M20 4v4h-4"/>',
  trash: '<path d="M4 7h16M10 4h4M6.5 7l1 13h9l1-13"/><path d="M10 11v5M14 11v5"/>',
  pencil: '<path d="M4 20l1-4L16.5 4.5a2.1 2.1 0 0 1 3 3L8 19l-4 1z"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  download: '<path d="M12 3v12M7 10l5 5 5-5"/><path d="M4 21h16"/>',
  eye: '<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/>',
  plug: '<path d="M9 7V3M15 7V3M7 7h10v3a5 5 0 0 1-10 0V7z"/><path d="M12 15v6"/>',
  alert: '<path d="M12 3 2.5 20h19L12 3z"/><path d="M12 9.5v4.5M12 17.2v.3"/>',
  key: '<circle cx="8" cy="15" r="4.5"/><path d="M11.5 11.5 20 3M17 6l2.5 2.5M14 9l2 2"/>',
  terminal: '<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="M7 9l3.5 3L7 15M12.5 15H17"/>',
  chart: '<path d="M4 20V10M10 20V4M16 20v-8M22 20H2"/>',
  arrow_right: '<path d="M4 12h15M13 6l6 6-6 6"/>',
  chevron_right: '<path d="M9 5l7 7-7 7"/>',
  chevron_down: '<path d="M5 9l7 7 7-7"/>',
  chevron_up: '<path d="M5 15l7-7 7 7"/>',
  clock: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7v5l3.5 2"/>',
  zap_off: '<path d="M9.5 4.5 13 2 4.5 13.5H11L10 22l4.5-7M4 3l17 17"/>',
  dot: '<circle cx="12" cy="12" r="4"/>',
  layers: '<path d="M12 3 2.5 8.5 12 14l9.5-5.5L12 3z"/><path d="m3.5 13 8.5 5 8.5-5M3.5 17.5 12 22.5l8.5-5"/>',
  gauge: '<path d="M4 14a8 8 0 1 1 16 0"/><path d="M12 14l4.5-4.5M3 19h18"/>',
}
</script>

<template>
  <svg
    :width="size" :height="size"
    viewBox="0 0 24 24" fill="none"
    stroke="currentColor" stroke-width="1.7"
    stroke-linecap="round" stroke-linejoin="round"
    v-html="icons[name] || icons.dot"
  />
</template>
