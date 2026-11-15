package io.github.autumnsgrove.outpost;

import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import net.fabricmc.loader.api.FabricLoader;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.concurrent.CompletableFuture;

/**
 * The daemon is the only thing that decides anything; this class is the wire.
 *
 * <p>Two fields come from {@code config/outpost.json}, written into the VPS by
 * cloud-init at boot: the URL to post to — the daemon over the tailnet — and
 * this session's token. The jar ships in the public {@code mods/} prefix, so a
 * long-lived secret baked into it would be a published secret. Nothing here
 * knows what a hold costs, who may set one, or how long is too long: it sends
 * the words and prints the sentence that comes back.
 */
final class Daemon {
	private static final Logger LOG = LoggerFactory.getLogger("outpost");

	// Every line this class hands back is prefixed here and nowhere else, so a
	// reply is attributable in chat whether it came from the daemon or from the
	// blank space where the daemon should have been.
	private static final String PREFIX = "Outpost: ";
	private static final String NOT_CONFIGURED =
		PREFIX + "no daemon URL for this server — config/outpost.json is missing or empty.";

	// Short on purpose. A command that cannot reach the daemon should say so in a
	// couple of seconds, not hold a chat reply for thirty.
	private static final HttpClient HTTP = HttpClient.newBuilder()
		.connectTimeout(Duration.ofSeconds(2))
		.build();

	private Daemon() {}

	/** Sends one command and returns the line to print. Never throws. */
	static CompletableFuture<String> call(String command, String arg, String player) {
		JsonObject config = readConfig();
		if (config == null) {
			return CompletableFuture.completedFuture(NOT_CONFIGURED);
		}
		String url = string(config, "url");
		String token = string(config, "token");
		if (url.isEmpty() || token.isEmpty()) {
			return CompletableFuture.completedFuture(NOT_CONFIGURED);
		}

		JsonObject body = new JsonObject();
		body.addProperty("token", token);
		body.addProperty("player", player);
		body.addProperty("command", command);
		body.addProperty("arg", arg);

		HttpRequest request;
		try {
			request = HttpRequest.newBuilder(URI.create(url + "/api/game/command"))
				.header("content-type", "application/json")
				.timeout(Duration.ofSeconds(5))
				.POST(HttpRequest.BodyPublishers.ofString(body.toString()))
				.build();
		} catch (IllegalArgumentException e) {
			// A malformed url in the file is a config error, not a crash.
			LOG.warn("outpost: bad url in config/outpost.json: {}", url);
			return CompletableFuture.completedFuture(PREFIX + "the daemon URL in config/outpost.json is not a URL.");
		}

		return HTTP.sendAsync(request, HttpResponse.BodyHandlers.ofString())
			.thenApply(response -> read(response.statusCode(), response.body()));
	}

	/** Reads the session's URL and token. Missing or unreadable is not fatal. */
	private static JsonObject readConfig() {
		Path file = FabricLoader.getInstance().getConfigDir().resolve("outpost.json");
		if (!Files.exists(file)) {
			// Expected on a server started by an Outpost whose config has no
			// server.game_endpoint: cloud-init writes this file only when there is
			// a daemon URL to write. Say what is wrong and what would fix it.
			LOG.warn("outpost: {} is missing, so /outpost cannot reach the daemon. "
				+ "cloud-init writes it from server.game_endpoint.", file);
			return null;
		}
		try {
			return JsonParser.parseString(Files.readString(file)).getAsJsonObject();
		} catch (Exception e) {
			LOG.warn("outpost: cannot read {}: {}", file, e.toString());
			return null;
		}
	}

	private static String string(JsonObject o, String key) {
		return o.has(key) && o.get(key).isJsonPrimitive() ? o.get(key).getAsString() : "";
	}

	/**
	 * Turns the HTTP answer into one sentence for chat.
	 *
	 * <p>The daemon already speaks in chat-sized lines and carries the whole
	 * meaning in {@code message}; the status code only matters when there is no
	 * body to read, which is what a proxy or a crash looks like.
	 */
	private static String read(int status, String body) {
		try {
			JsonObject out = JsonParser.parseString(body).getAsJsonObject();
			for (String key : new String[] {"message", "error"}) {
				if (out.has(key) && out.get(key).isJsonPrimitive()) {
					return PREFIX + out.get(key).getAsString();
				}
			}
		} catch (Exception ignored) {
			// Not JSON, or not the shape we expect: fall through to the status.
		}
		if (status == 401) {
			return PREFIX + "the daemon did not recognise this server's token.";
		}
		return PREFIX + "the daemon answered " + status + ".";
	}
}
