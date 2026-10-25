package io.freemytv;

import android.os.SystemClock;
import android.text.TextUtils;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.net.URL;
import java.net.URLConnection;
import java.nio.charset.StandardCharsets;
import java.text.NumberFormat;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.Executor;

import javax.net.ssl.HttpsURLConnection;

/**
 * This fork (HOMELAB.md): the numbers on the drawer's Homelab card, from the aggregator's GET /stats (JSON version 1)
 * on the LAN. The aggregator does the maths; the TV only shows its numbers. Fetched every 60 s and only while the home
 * screen is up (HomeView's tick, through HomeData), never in the background. A failed fetch keeps the last good numbers
 * and marks them stale, and so does an answer made over 3 minutes ago: a frozen number must never look like a live one.
 */
final class Homelab {
    /** show_homelab=true and a homelab_url in config.properties. Otherwise the drawer is upstream's. */
    static final boolean ON = Config.SHOW_HOMELAB && !Config.HOMELAB_URL.trim().isEmpty();
    /** A number the aggregator sent as null (pve2 has no temperature source yet) or left out. */
    static final int NONE = Integer.MIN_VALUE;
    private static final long EVERY = 60_000L, STALE_AFTER = 3 * 60_000L;
    /** The answer is about 2 KB: one this big isn't it. */
    private static final int MAX_BYTES = 256 * 1024;

    static final class Node {
        String name = "";
        boolean online = true;
        int cpu = NONE, mem = NONE, temp = NONE;
    }

    /** One good answer. Never changed once made: the screen draws it while the next one is fetched. */
    static final class Data {
        long at;    // when it arrived: SystemClock.elapsedRealtime(), so the time since needs no settable clock
        long age;   // how old the numbers were when they arrived, ms (see parse)
        int cpu = NONE, mem = NONE, tank = NONE;
        final List<Node> nodes = new ArrayList<>();
        int guests, running;
        final List<String> notRunning = new ArrayList<>();
        int charge = NONE, runtime = NONE;
        boolean onBattery;
        String error = "";  // the aggregator's first error: shown when no node answered
    }

    /** The last good answer; null until the first one. */
    volatile Data data;
    private volatile boolean failed, fetching, staleShown;
    private volatile long triedAt;
    private String lastError = "";  // only the fetching thread uses it

    /** Nothing yet, the last fetch failed, or the numbers are over 3 minutes old. */
    boolean stale() {
        Data d = data;
        return d == null || failed || d.age + SystemClock.elapsedRealtime() - d.at > STALE_AFTER;
    }

    /**
     * The home screen calls this when it appears and on its 15 s tick: it fetches when the last try is a minute old.
     * `changed` runs after every try (a failure makes the card stale) and when the answer ages past 3 minutes.
     */
    void refreshIfStale(Executor io, Runnable changed) {
        if (!ON) return;
        long now = System.currentTimeMillis();
        boolean stale = stale();
        if (stale != staleShown) {
            staleShown = stale;
            changed.run();
        }
        // 5 s early: the tick comes every 15 s, so waiting the full minute would make it 75 s. A negative age (the
        // TV's clock went back) counts as due.
        if (fetching || now - triedAt < EVERY - 5_000 && now >= triedAt) return;
        fetching = true;
        triedAt = now;
        io.execute(() -> {
            try {
                long[] served = {0};
                String body = get(Config.HOMELAB_URL.trim(), served);
                data = parse(body, served[0], System.currentTimeMillis());
                failed = false;
                lastError = "";
            } catch (Exception | StackOverflowError e) {   // never a crash: 3 in 5 minutes send the TV back to stock
                failed = true;
                String error = e.getClass().getSimpleName() + ": " + e.getMessage();
                // Once, not every minute. Android's connect errors name the TV's port, new on every try ("... from
                // /10.0.0.218 (port 43822) after 5000ms"), so what comes from there on doesn't make it another error.
                int from = error.indexOf(" from ");
                String key = from < 0 ? error : error.substring(0, from);
                if (!key.equals(lastError)) AppLog.w("homelab: " + error);
                lastError = key;
            } finally {
                fetching = false;
                staleShown = stale();
                changed.run();
            }
        });
    }

    /**
     * JSON version 1 (HOMELAB.md §5). Fields it doesn't know are ignored; a missing or null section is left empty.
     * `served` is the answer's HTTP Date header (0 when there's none), `now` the TV's clock when it arrived.
     */
    static Data parse(String body, long served, long now) throws Exception {
        JSONObject o = new JSONObject(body);
        int version = o.optInt("version", 0);
        // A breaking change bumps the version: refuse it (stale, last good numbers kept) rather than misread it.
        if (version != 1) throw new IOException("version " + version + ", this build reads 1");
        Data d = new Data();
        d.at = SystemClock.elapsedRealtime();
        // "2026-10-24T19:40:00-04:00" (Go's RFC 3339). Without it there's no telling how old the numbers are.
        long updated = OffsetDateTime.parse(text(o, "updated")).toInstant().toEpochMilli();
        // Their age on one clock: the Date header comes from NPM, on the aggregator's own machine (LXC 100 runs on
        // pve's kernel clock), so a TV clock that's a few minutes off doesn't matter. Without it, the TV's clock does.
        d.age = (served > 0 ? served : now) - updated;
        JSONObject overall = o.optJSONObject("overall");
        if (overall != null) {
            d.cpu = number(overall, "cpu_pct");
            d.mem = number(overall, "mem_pct");
        }
        JSONArray nodes = o.optJSONArray("nodes");
        for (int i = 0; nodes != null && i < nodes.length(); i++) {
            JSONObject n = nodes.optJSONObject(i);
            if (n == null) continue;
            Node node = new Node();
            node.name = text(n, "name");
            node.online = n.optBoolean("online", true);
            node.cpu = number(n, "cpu_pct");
            node.mem = number(n, "mem_pct");
            node.temp = number(n, "temp_c");
            d.nodes.add(node);
        }
        JSONArray guests = o.optJSONArray("guests");
        for (int i = 0; guests != null && i < guests.length(); i++) {
            JSONObject g = guests.optJSONObject(i);
            if (g == null) continue;
            d.guests++;
            if ("running".equals(text(g, "status"))) d.running++;
            else d.notRunning.add(text(g, "name").isEmpty() ? String.valueOf(g.optInt("vmid")) : text(g, "name"));
        }
        JSONObject storage = o.optJSONObject("storage");
        if (storage != null) d.tank = number(storage, "tank_used_pct");
        JSONObject ups = o.optJSONObject("ups");
        if (ups != null) {
            d.charge = number(ups, "charge_pct");
            d.runtime = number(ups, "runtime_min");
            // NUT's status words: OL on line power, OB on battery, LB low battery ("OB LB").
            String status = " " + text(ups, "status") + " ";
            d.onBattery = status.contains(" OB ") || status.contains(" LB ");
        }
        JSONArray errors = o.optJSONArray("errors");
        Object error = errors == null ? null : errors.opt(0);
        if (error instanceof String) d.error = (String) error;
        return d;
    }

    /** A whole number, or NONE for null, missing, or anything that isn't a sensible number. */
    private static int number(JSONObject o, String key) {
        Object v = o.opt(key);
        if (!(v instanceof Number)) return NONE;
        double x = ((Number) v).doubleValue();
        return x >= -1e6 && x <= 1e6 ? (int) Math.round(x) : NONE;   // also NaN
    }

    /** The string, or "" for null or missing (optString would give "null"). */
    private static String text(JSONObject o, String key) {
        Object v = o.opt(key);
        return v instanceof String ? (String) v : "";
    }

    /** The answer's body; `served` gets its HTTP Date header (epoch ms, 0 when there's none). */
    private static String get(String url, long[] served) throws IOException {
        URLConnection u = new URL(url).openConnection();
        // HTTPS only (HOMELAB.md §4): NPM's certificate is checked as for any site, and Android refuses cleartext.
        if (!(u instanceof HttpsURLConnection)) throw new IOException("homelab_url must start with https://");
        HttpsURLConnection c = (HttpsURLConnection) u;
        c.setConnectTimeout(5000);
        c.setReadTimeout(5000);
        c.setUseCaches(false);
        c.setRequestProperty("User-Agent", "FreeMyTV");
        c.setRequestProperty("Accept", "application/json");
        try {
            if (c.getResponseCode() != 200) throw new IOException("HTTP " + c.getResponseCode());
            served[0] = c.getDate();
            try (InputStream in = c.getInputStream()) {
                ByteArrayOutputStream out = new ByteArrayOutputStream();
                byte[] buffer = new byte[8192];
                int n;
                while ((n = in.read(buffer)) > 0) {
                    out.write(buffer, 0, n);
                    if (out.size() > MAX_BYTES) throw new IOException("answer over " + MAX_BYTES + " bytes");
                }
                return out.toString(StandardCharsets.UTF_8.name());
            }
        } finally {
            c.disconnect();
        }
    }

    // MARK: - Text (the card's and TalkBack's)

    /** "13%", "13 %" or "%13", as the language writes it; "—" for no number. */
    static String percent(int v) {
        if (v == NONE) return "—";
        return Ui.once("percent", v, () -> NumberFormat.getPercentInstance(Ui.locale()).format(v / 100.0));
    }

    /** In °C whatever the weather card uses: computer temperatures are (lm-sensors, Proxmox). */
    static String celsius(int v) {
        return v == NONE ? "—" : v + "°C";
    }

    static String cpu(int v) {
        return Ui.trf("CPU {pct}", "pct", percent(v));
    }

    static String ram(int v) {
        return Ui.trf("RAM {pct}", "pct", percent(v));
    }

    /** "Guests 4/4"; "" when the aggregator listed none (Proxmox didn't answer). */
    static String guests(Data d) {
        return d.guests == 0 ? "" : Ui.trf("Guests {n}/{total}", "n", d.running, "total", d.guests);
    }

    /** "UPS 100%", then the runtime NUT gives ("64 min", "1 h 4 min") when there's room for it. */
    static String ups(Data d, boolean runtime) {
        String s = Ui.trf("UPS {pct}", "pct", percent(d.charge));
        return runtime && d.runtime != NONE && d.runtime >= 0 ? s + " · " + SleepTimer.duration(d.runtime) : s;
    }

    /** `tank` is the ZFS pool's name, not a word, so it isn't translated. */
    static String tank(Data d) {
        return "tank " + percent(d.tank);
    }

    /** The card as TalkBack reads it, in the same words. */
    static String spoken(Data d, boolean stale) {
        StringBuilder s = new StringBuilder(Ui.tr("Homelab"));
        if (d == null) return s.append(". ").append(Ui.tr("No homelab data right now.")).toString();
        if (stale) s.append(", ").append(Ui.tr("Stale"));
        s.append(". ").append(cpu(d.cpu)).append(", ").append(ram(d.mem)).append('.');
        for (Node n : d.nodes) {
            s.append(' ').append(n.name).append(": ");
            if (!n.online) s.append(Ui.tr("Offline"));
            else s.append(cpu(n.cpu)).append(", ").append(ram(n.mem));
            if (n.online && n.temp != NONE) s.append(", ").append(celsius(n.temp));
            s.append('.');
        }
        if (d.guests > 0) s.append(' ').append(guests(d)).append('.');
        if (!d.notRunning.isEmpty()) {
            s.append(' ').append(Ui.trf("Not running: {names}", "names", TextUtils.join(", ", d.notRunning))).append('.');
        }
        return s.append(' ').append(ups(d, true)).append(". ").append(tank(d)).append('.').toString();
    }
}
