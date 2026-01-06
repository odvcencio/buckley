import { useState, useCallback, useEffect } from 'react'
import { client } from '../lib/grpc'

interface PushState {
  supported: boolean
  permission: NotificationPermission
  subscribed: boolean
  loading: boolean
  error: string | null
}

// Convert URL-safe base64 to Uint8Array for applicationServerKey
function urlBase64ToUint8Array(base64String: string): Uint8Array {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4)
  const base64 = (base64String + padding)
    .replace(/-/g, '+')
    .replace(/_/g, '/')

  const rawData = window.atob(base64)
  const outputArray = new Uint8Array(rawData.length)

  for (let i = 0; i < rawData.length; ++i) {
    outputArray[i] = rawData.charCodeAt(i)
  }
  return outputArray
}

export function usePushNotifications() {
  const [state, setState] = useState<PushState>({
    supported: false,
    permission: 'default',
    subscribed: false,
    loading: false,
    error: null,
  })

  // Check initial state
  useEffect(() => {
    const checkSupport = async () => {
      const supported =
        'serviceWorker' in navigator &&
        'PushManager' in window &&
        'Notification' in window

      if (!supported) {
        setState((s) => ({ ...s, supported: false }))
        return
      }

      const permission = Notification.permission

      // Check if already subscribed
      const registration = await navigator.serviceWorker.ready
      const subscription = await registration.pushManager.getSubscription()

      setState({
        supported: true,
        permission,
        subscribed: !!subscription,
        loading: false,
        error: null,
      })
    }

    checkSupport()
  }, [])

  // Register service worker
  useEffect(() => {
    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.register('/sw.js').catch((err) => {
        console.error('Service Worker registration failed:', err)
      })
    }
  }, [])

  // Subscribe to push notifications
  const subscribe = useCallback(async () => {
    if (!state.supported) return

    setState((s) => ({ ...s, loading: true, error: null }))

    try {
      // Request notification permission
      const permission = await Notification.requestPermission()
      if (permission !== 'granted') {
        setState((s) => ({
          ...s,
          permission,
          loading: false,
          error: 'Notification permission denied',
        }))
        return
      }

      // Get VAPID public key from server
      const { publicKey } = await client.getVAPIDPublicKey()
      if (!publicKey) {
        throw new Error('No VAPID public key available')
      }

      // Subscribe to push manager
      const registration = await navigator.serviceWorker.ready
      const applicationServerKey = urlBase64ToUint8Array(publicKey)
      const subscription = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: applicationServerKey as BufferSource,
      })

      // Send subscription to server
      const json = subscription.toJSON()
      await client.subscribePush({
        endpoint: json.endpoint,
        p256dh: json.keys?.p256dh || '',
        auth: json.keys?.auth || '',
        userAgent: navigator.userAgent,
      })

      setState({
        supported: true,
        permission: 'granted',
        subscribed: true,
        loading: false,
        error: null,
      })
    } catch (err) {
      setState((s) => ({
        ...s,
        loading: false,
        error: err instanceof Error ? err.message : 'Failed to subscribe',
      }))
    }
  }, [state.supported])

  // Unsubscribe from push notifications
  const unsubscribe = useCallback(async () => {
    if (!state.subscribed) return

    setState((s) => ({ ...s, loading: true, error: null }))

    try {
      const registration = await navigator.serviceWorker.ready
      const subscription = await registration.pushManager.getSubscription()

      if (subscription) {
        // Unsubscribe locally
        await subscription.unsubscribe()

        // Remove from server
        const json = subscription.toJSON()
        await client.unsubscribePush({
          endpoint: json.endpoint,
        })
      }

      setState((s) => ({
        ...s,
        subscribed: false,
        loading: false,
        error: null,
      }))
    } catch (err) {
      setState((s) => ({
        ...s,
        loading: false,
        error: err instanceof Error ? err.message : 'Failed to unsubscribe',
      }))
    }
  }, [state.subscribed])

  return {
    ...state,
    subscribe,
    unsubscribe,
  }
}
