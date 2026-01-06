/**
 * Buckley Service Worker
 * Handles push notifications and offline caching
 */

const CACHE_NAME = 'buckley-v1'
const STATIC_ASSETS = [
  '/',
  '/index.html',
  '/manifest.json',
  '/favicon.svg',
]

// Install event - cache static assets
self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => {
      return cache.addAll(STATIC_ASSETS)
    })
  )
  // Activate immediately
  self.skipWaiting()
})

// Activate event - clean old caches
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((cacheNames) => {
      return Promise.all(
        cacheNames
          .filter((name) => name !== CACHE_NAME)
          .map((name) => caches.delete(name))
      )
    })
  )
  // Claim all clients immediately
  self.clients.claim()
})

// Fetch event - serve from cache, fallback to network
self.addEventListener('fetch', (event) => {
  // Only cache GET requests
  if (event.request.method !== 'GET') return

  event.respondWith(
    caches.match(event.request).then((response) => {
      // Return cached response or fetch from network
      return response || fetch(event.request).then((fetchResponse) => {
        // Only cache same-origin requests
        if (fetchResponse.ok && event.request.url.startsWith(self.location.origin)) {
          const responseClone = fetchResponse.clone()
          caches.open(CACHE_NAME).then((cache) => {
            cache.put(event.request, responseClone)
          })
        }
        return fetchResponse
      })
    })
  )
})

// Push notification received
self.addEventListener('push', (event) => {
  if (!event.data) return

  let data
  try {
    data = event.data.json()
  } catch {
    data = {
      title: 'Buckley',
      body: event.data.text(),
    }
  }

  const options = {
    body: data.body || 'New notification',
    icon: '/favicon.svg',
    badge: '/favicon.svg',
    vibrate: [200, 100, 200],
    tag: data.tag || 'buckley-notification',
    renotify: true,
    requireInteraction: data.requireInteraction || false,
    data: {
      url: data.url || '/',
      approvalId: data.approvalId,
      sessionId: data.sessionId,
      type: data.type,
    },
    actions: data.actions || [],
  }

  // Add approval actions if this is an approval notification
  if (data.type === 'approval') {
    options.requireInteraction = true
    options.actions = [
      { action: 'approve', title: 'Approve' },
      { action: 'reject', title: 'Reject' },
    ]
  }

  event.waitUntil(
    self.registration.showNotification(data.title || 'Buckley', options)
  )
})

// Notification click handler
self.addEventListener('notificationclick', (event) => {
  event.notification.close()

  const data = event.notification.data
  const action = event.action

  // Handle approval actions
  if (data.approvalId && (action === 'approve' || action === 'reject')) {
    event.waitUntil(handleApprovalAction(data.approvalId, action))
    return
  }

  // Open app on click
  event.waitUntil(
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clientList) => {
      // Focus existing window if available
      for (const client of clientList) {
        if (client.url.includes(self.location.origin) && 'focus' in client) {
          client.focus()
          if (data.sessionId) {
            client.postMessage({
              type: 'navigate',
              sessionId: data.sessionId,
              approvalId: data.approvalId,
            })
          }
          return
        }
      }

      // Open new window
      const url = data.url || '/'
      return clients.openWindow(url)
    })
  )
})

// Handle approval action from notification
async function handleApprovalAction(approvalId, action) {
  const endpoint = action === 'approve' ? 'ApproveToolCall' : 'RejectToolCall'
  const body = {
    approvalId,
    decidedBy: 'push-notification',
  }

  if (action === 'reject') {
    body.reason = 'Rejected via push notification'
  }

  try {
    const response = await fetch(`/buckley.ipc.v1.BuckleyIPC/${endpoint}`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Connect-Protocol-Version': '1',
      },
      body: JSON.stringify(body),
    })

    if (!response.ok) {
      console.error('Failed to handle approval:', await response.text())
    }
  } catch (err) {
    console.error('Error handling approval action:', err)
  }
}

// Notification close handler
self.addEventListener('notificationclose', (event) => {
  // Track notification dismissal if needed
  console.log('Notification closed:', event.notification.tag)
})

// Message handler for client communication
self.addEventListener('message', (event) => {
  if (event.data.type === 'skipWaiting') {
    self.skipWaiting()
  }
})
