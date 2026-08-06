package life.meyers.mecha

import android.content.SharedPreferences
import org.junit.Assert.assertEquals
import org.junit.Test

class DraftStoreTest {
    @Test
    fun normalizesEscapedNewlineText() {
        assertEquals("first\nsecond", normalizeEscapedNewlines("first\\nsecond"))
        assertEquals("first\nsecond", normalizeEscapedNewlines("first\\rsecond"))
        assertEquals("first\nsecond", normalizeEscapedNewlines("first\\r\\nsecond"))
        assertEquals("first\nsecond", normalizeEscapedNewlines("first\nsecond"))
    }

    @Test
    fun restoresEachRouterAndPeerDraftAfterStoreRecreation() {
        val preferences = FakeSharedPreferences()
        DraftStore(preferences).apply {
            save("donuts:8765", "supervisor", "supervisor draft")
            save("donuts:8765", "worker", "worker draft")
            save("other:8765", "supervisor", "other router draft")
        }

        val restarted = DraftStore(preferences)

        assertEquals("supervisor draft", restarted.get("donuts:8765", "supervisor"))
        assertEquals("worker draft", restarted.get("donuts:8765", "worker"))
        assertEquals("other router draft", restarted.get("other:8765", "supervisor"))
    }

    @Test
    fun clearsOnlyTheSentDraft() {
        val preferences = FakeSharedPreferences()
        val drafts = DraftStore(preferences)
        drafts.save("donuts:8765", "supervisor", "send this")
        drafts.save("donuts:8765", "worker", "keep this")

        drafts.clear("donuts:8765", "supervisor")

        assertEquals("", drafts.get("donuts:8765", "supervisor"))
        assertEquals("keep this", drafts.get("donuts:8765", "worker"))
    }

    @Test
    fun successfulSendClearsOnlyTheUnchangedDraft() {
        val drafts = DraftStore(FakeSharedPreferences())
        drafts.save("donuts:8765", "supervisor", "sent text")
        assertEquals(true, drafts.clearIfUnchanged("donuts:8765", "supervisor", "sent text"))
        assertEquals("", drafts.get("donuts:8765", "supervisor"))

        drafts.save("donuts:8765", "supervisor", "newer edit")
        assertEquals(false, drafts.clearIfUnchanged("donuts:8765", "supervisor", "sent text"))
        assertEquals("newer edit", drafts.get("donuts:8765", "supervisor"))
    }

    @Test
    fun retainsOnlyTheMostRecentFiftyDraftsPerRouter() {
        val drafts = DraftStore(FakeSharedPreferences())
        drafts.save("other:8765", "peer-1", "other router draft")
        (1..51).forEach { drafts.save("donuts:8765", "peer-$it", "draft-$it") }

        assertEquals("", drafts.get("donuts:8765", "peer-1"))
        assertEquals("draft-2", drafts.get("donuts:8765", "peer-2"))
        assertEquals("draft-51", drafts.get("donuts:8765", "peer-51"))
        assertEquals("other router draft", drafts.get("other:8765", "peer-1"))
    }
}

private class FakeSharedPreferences : SharedPreferences {
    private val values = mutableMapOf<String, Any?>()

    override fun getAll(): Map<String, *> = values.toMap()
    override fun getString(key: String?, defValue: String?): String? = values[key] as? String ?: defValue
    override fun getStringSet(key: String?, defValues: Set<String>?): Set<String>? = defValues
    override fun getInt(key: String?, defValue: Int) = values[key] as? Int ?: defValue
    override fun getLong(key: String?, defValue: Long) = values[key] as? Long ?: defValue
    override fun getFloat(key: String?, defValue: Float) = values[key] as? Float ?: defValue
    override fun getBoolean(key: String?, defValue: Boolean) = values[key] as? Boolean ?: defValue
    override fun contains(key: String?) = values.containsKey(key)
    override fun edit(): SharedPreferences.Editor = Editor()
    override fun registerOnSharedPreferenceChangeListener(listener: SharedPreferences.OnSharedPreferenceChangeListener?) = Unit
    override fun unregisterOnSharedPreferenceChangeListener(listener: SharedPreferences.OnSharedPreferenceChangeListener?) = Unit

    private inner class Editor : SharedPreferences.Editor {
        private val edits = mutableMapOf<String, Any?>()
        private var clear = false

        override fun putString(key: String?, value: String?) = apply { edits[key.orEmpty()] = value }
        override fun putStringSet(key: String?, values: Set<String>?) = apply { edits[key.orEmpty()] = values }
        override fun putInt(key: String?, value: Int) = apply { edits[key.orEmpty()] = value }
        override fun putLong(key: String?, value: Long) = apply { edits[key.orEmpty()] = value }
        override fun putFloat(key: String?, value: Float) = apply { edits[key.orEmpty()] = value }
        override fun putBoolean(key: String?, value: Boolean) = apply { edits[key.orEmpty()] = value }
        override fun remove(key: String?) = apply { edits[key.orEmpty()] = null }
        override fun clear() = apply { clear = true }
        override fun commit(): Boolean {
            if (clear) values.clear()
            edits.forEach { (key, value) -> if (value == null) values.remove(key) else values[key] = value }
            return true
        }
        override fun apply() = commit().let { }
    }
}
