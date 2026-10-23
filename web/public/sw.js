// Railse Service Worker：Task 10 加入推送处理。现在只让页面能加到主屏幕。
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()))
