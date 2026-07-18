package life.meyers.mecha

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import org.json.JSONObject
import java.util.Timer
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import kotlin.concurrent.scheduleAtFixedRate

class MailboxConnectionService : Service() {
    private data class Session(val attempt: RouterConnectionAttempt, var socket: WebSocket? = null, var timer: Timer? = null)

    private val client = OkHttpClient()
    private val reconnectHandler = Handler(Looper.getMainLooper())
    private val coordinator = RouterConnectionCoordinator()
    private val sessions = ConcurrentHashMap<String, Session>()
    private lateinit var agentId: String
    private lateinit var instanceId: String

    override fun onCreate() {
        super.onCreate()
        agentId = prefs().getString("agent_id", null).orEmpty()
        instanceId = UUID.randomUUID().toString()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopAll()
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return START_NOT_STICKY
        }
        reconcileConfiguredRouters()
        return START_STICKY
    }

    override fun onDestroy() {
        stopAll()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun reconcileConfiguredRouters() {
        agentId = prefs().getString("agent_id", null).orEmpty()
        val configured = configuredRouters()
        if (agentId.isBlank() || configured.isEmpty()) {
            stopSelf()
            return
        }
        startAsForeground("Connecting to your agents…")
        sessions.keys.toList().filter { it !in configured }.forEach(::stopEndpoint)
        configured.filterNot(sessions::containsKey).forEach(::startEndpoint)
    }

    private fun startEndpoint(endpoint: String) {
        val attempt = coordinator.begin(endpoint)
        val session = Session(attempt)
        sessions[endpoint] = session
        updateSelectedConnection(attempt, "Connecting to your agents…")
        session.socket = client.newWebSocket(Request.Builder().url("ws://$endpoint/v1/agents").build(), object : WebSocketListener() {
            private var leaseId = ""

            override fun onOpen(webSocket: WebSocket, response: Response) {
                if (!coordinator.isCurrent(attempt)) return
                webSocket.send(registerFrame())
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                if (!coordinator.isCurrent(attempt)) return
                val frame = try { JSONObject(text) } catch (_: Exception) {
                    updateSelectedConnection(attempt, "Router sent an invalid response")
                    return
                }
                when (frame.optString("type")) {
                    "registered" -> {
                        val returnedLease = frame.optString("lease_id")
                        if (returnedLease.isBlank()) updateSelectedConnection(attempt, "Router rejected this username")
                        else if (leaseId.isBlank()) {
                            leaseId = returnedLease
                            updateSelectedConnection(attempt, "Connected as $agentId")
                            startLeaseRenewal(session, leaseId)
                            discoverPeers(attempt)
                        }
                    }
                    "lease_rejected" -> updateSelectedConnection(attempt, "Username is already connected elsewhere")
                    "error" -> updateSelectedConnection(attempt, "Router error: ${frame.optString("code", "unknown")}")
                    "message" -> receiveMessage(webSocket, frame, attempt, leaseId)
                }
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) = reconnect(attempt)
            override fun onFailure(webSocket: WebSocket, throwable: Throwable, response: Response?) = reconnect(attempt)
            override fun onMessage(webSocket: WebSocket, bytes: ByteString) = Unit
        })
    }

    private fun registerFrame() = JSONObject()
        .put("version", "v1").put("frame_id", UUID.randomUUID().toString()).put("type", "register")
        .put("agent_id", agentId).put("instance_id", instanceId)
        .put("agent_role", "user_interaction").put("client_type", "android").put("ack_confirmation", "correlated_v1")
        .toString()

    private fun receiveMessage(socket: WebSocket, frame: JSONObject, attempt: RouterConnectionAttempt, leaseId: String) {
        if (!coordinator.isCurrent(attempt) || leaseId.isBlank()) return
        val message = frame.optJSONObject("message") ?: return
        val sender = message.optString("from")
        val body = message.optString("body")
        val id = message.optString("id", UUID.randomUUID().toString())
        if (sender.isBlank()) return
        val stored = ConversationStore.append(this, attempt.endpoint, sender,
            ChatMessage(id, sender, body, Direction.INCOMING, java.time.Instant.now().toString()))
        if (!stored || !coordinator.isCurrent(attempt)) return
        broadcastUpdate(attempt.endpoint, sender)
        if (ForegroundNotificationPolicy.shouldPostInboundNotification((application as MechaApplication).isForeground())) {
            postInboundNotification(attempt.endpoint, sender, body)
        }
        val receipt = JSONObject().put("version", "v1").put("agent_id", agentId).put("instance_id", instanceId)
            .put("lease_id", leaseId).put("message_id", id).put("attempt_number", frame.optInt("attempt_number", 1))
        socket.send(receipt.put("frame_id", UUID.randomUUID().toString()).put("type", "injection_requested").toString())
        socket.send(receipt.put("frame_id", UUID.randomUUID().toString()).put("type", "ack").toString())
    }

    private fun startLeaseRenewal(session: Session, leaseId: String) {
        session.timer?.cancel()
        session.timer = Timer().apply {
            scheduleAtFixedRate(20_000, 20_000) {
                if (!coordinator.isCurrent(session.attempt)) return@scheduleAtFixedRate
                session.socket?.send(JSONObject().put("version", "v1").put("frame_id", UUID.randomUUID().toString())
                    .put("type", "lease_renew").put("agent_id", agentId).put("instance_id", instanceId).put("lease_id", leaseId).toString())
            }
            scheduleAtFixedRate(60_000, 60_000) { discoverPeers(session.attempt) }
        }
    }

    private fun discoverPeers(attempt: RouterConnectionAttempt) = Thread {
        try {
            client.newCall(Request.Builder().url("http://${attempt.endpoint}/v1/agents").build()).execute().use { response ->
                if (!response.isSuccessful || !coordinator.isCurrent(attempt)) return@use
                val agents = JSONObject(response.body?.string().orEmpty()).optJSONArray("agents") ?: return@use
                val peers = buildSet {
                    for (index in 0 until agents.length()) agents.optJSONObject(index)?.optString("agent_id")
                        ?.takeIf { it.isNotBlank() && it != agentId }?.let(::add)
                    addAll(ConversationStore.knownPeers(this@MailboxConnectionService, attempt.endpoint).filter { it != agentId })
                }
                if (!coordinator.isCurrent(attempt)) return@use
                prefs().edit().putStringSet("known_peers:${attempt.endpoint}", peers).commit()
                broadcastUpdate(attempt.endpoint)
            }
        } catch (_: Exception) { }
    }.start()

    private fun reconnect(attempt: RouterConnectionAttempt) {
        if (!coordinator.isCurrent(attempt)) return
        updateSelectedConnection(attempt, "Router unavailable; retrying…")
        reconnectHandler.postDelayed({
            if (!coordinator.isCurrent(attempt) || attempt.endpoint !in configuredRouters()) return@postDelayed
            stopEndpoint(attempt.endpoint)
            startEndpoint(attempt.endpoint)
        }, RECONNECT_DELAY_MS)
    }

    private fun stopEndpoint(endpoint: String) {
        coordinator.remove(endpoint)
        sessions.remove(endpoint)?.let { session ->
            session.timer?.cancel()
            session.socket?.cancel()
        }
    }

    private fun stopAll() {
        reconnectHandler.removeCallbacksAndMessages(null)
        sessions.keys.toList().forEach(::stopEndpoint)
    }

    private fun updateSelectedConnection(attempt: RouterConnectionAttempt, status: String) {
        if (!coordinator.isCurrent(attempt)) return
        val selected = selectedRouter()
        if (attempt.endpoint == selected) {
            (getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).notify(CONNECTION_NOTIFICATION_ID, connectionNotification(status))
            sendBroadcast(Intent(ACTION_STATE_CHANGED).setPackage(packageName)
                .putExtra(EXTRA_CONNECTION, status).putExtra(EXTRA_ROUTER, attempt.endpoint))
        }
    }

    private fun broadcastUpdate(router: String, peer: String? = null) {
        sendBroadcast(Intent(ACTION_STATE_CHANGED).setPackage(packageName).putExtra(EXTRA_ROUTER, router).apply {
            if (peer != null) putExtra(EXTRA_PEER, peer)
        })
    }

    private fun startAsForeground(status: String) {
        createNotificationChannels()
        val notification = connectionNotification(status)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) startForeground(CONNECTION_NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING)
        else startForeground(CONNECTION_NOTIFICATION_ID, notification)
    }

    private fun createNotificationChannels() {
        val manager = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        manager.createNotificationChannel(NotificationChannel(CONNECTION_NOTIFICATION_CHANNEL, "Connection", NotificationManager.IMPORTANCE_LOW))
        manager.createNotificationChannel(NotificationChannel(MESSAGE_NOTIFICATION_CHANNEL, "Messages", NotificationManager.IMPORTANCE_DEFAULT))
    }

    private fun connectionNotification(status: String) = Notification.Builder(this, CONNECTION_NOTIFICATION_CHANNEL)
        .setSmallIcon(android.R.drawable.stat_notify_sync).setContentTitle("Mecha is active").setContentText(status)
        .setContentIntent(PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)).setOngoing(true).build()

    private fun postInboundNotification(router: String, from: String, body: String) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return
        val conversationId = ConversationStore.conversationId(this, router, from)
        val intent = Intent(this, MainActivity::class.java).setAction("mecha.conversation.$conversationId")
            .setData(android.net.Uri.parse("mecha://conversation/$conversationId"))
            .putExtra(EXTRA_ROUTER, router)
            .putExtra("peer_id", from)
        val notification = Notification.Builder(this, MESSAGE_NOTIFICATION_CHANNEL).setSmallIcon(android.R.drawable.stat_notify_chat)
            .setContentTitle(humanName(from)).setSubText("via ${routerDisplay(router)}").setContentText(body)
            .setContentIntent(PendingIntent.getActivity(this, conversationId.hashCode(), intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)).setAutoCancel(true).build()
        (getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).notify(conversationId, 1, notification)
    }

    private fun configuredRouters(): Set<String> = prefs().getStringSet("router_endpoints", emptySet()).orEmpty().mapNotNull { stored ->
        stored.substringAfter('\u001f', stored).takeIf { it.isNotBlank() }
    }.toSet()
    private fun selectedRouter() = prefs().getString("active_router", null).orEmpty()
    private fun routerDisplay(endpoint: String) = prefs().getStringSet("router_endpoints", emptySet()).orEmpty()
        .firstOrNull { it.substringAfter('\u001f', it) == endpoint }?.substringBefore('\u001f') ?: endpoint
    private fun prefs() = getSharedPreferences("mecha", MODE_PRIVATE)

    companion object {
        const val ACTION_CONNECT = "life.meyers.mecha.CONNECT"
        const val ACTION_STOP = "life.meyers.mecha.STOP"
        const val ACTION_STATE_CHANGED = "life.meyers.mecha.STATE_CHANGED"
        const val ACTION_REFRESH = "life.meyers.mecha.REFRESH"
        const val EXTRA_ROUTER = "router"
        const val EXTRA_CONNECTION = "connection"
        const val EXTRA_PEER = "peer"
        private const val RECONNECT_DELAY_MS = 1_000L
        private const val CONNECTION_NOTIFICATION_ID = 10
        private const val CONNECTION_NOTIFICATION_CHANNEL = "connection"
        private const val MESSAGE_NOTIFICATION_CHANNEL = "messages"
        fun connect(context: Context, router: String) = context.startForegroundService(Intent(context, MailboxConnectionService::class.java).setAction(ACTION_CONNECT).putExtra(EXTRA_ROUTER, router))
        fun refresh(context: Context) = context.startService(Intent(context, MailboxConnectionService::class.java).setAction(ACTION_REFRESH))
        fun stop(context: Context) = context.startService(Intent(context, MailboxConnectionService::class.java).setAction(ACTION_STOP))
    }
}
