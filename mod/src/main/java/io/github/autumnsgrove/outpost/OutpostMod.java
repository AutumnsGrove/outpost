package io.github.autumnsgrove.outpost;

import com.mojang.brigadier.CommandDispatcher;
import com.mojang.brigadier.arguments.StringArgumentType;
import net.fabricmc.api.ModInitializer;
import net.fabricmc.fabric.api.command.v2.CommandRegistrationCallback;
import net.minecraft.server.MinecraftServer;
import net.minecraft.server.command.ServerCommandSource;
import net.minecraft.text.Text;

import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;

import static net.minecraft.server.command.CommandManager.argument;
import static net.minecraft.server.command.CommandManager.literal;

/**
 * {@code /outpost cost|status|hold|cancel}, and nothing else.
 *
 * <p>Registered through Fabric API's command callback, which hands over
 * Brigadier — Minecraft's own command parser — so the arguments get parsed and
 * tab-completed by the game rather than by a string split.
 *
 * <p>Every reply is produced by the daemon and printed here verbatim. That is
 * the whole design: the side of the wire with tests decides, and this side has
 * no opinion it could get wrong.
 */
public class OutpostMod implements ModInitializer {
	/** Level 2 is "op". A rank, not an identity: the daemon checks the ops list too. */
	private static final int OP = 2;

	private static final String USAGE = "Outpost: /outpost cost | status | hold <48h> | cancel";

	@Override
	public void onInitialize() {
		CommandRegistrationCallback.EVENT.register((dispatcher, registries, environment) -> register(dispatcher));
	}

	private static void register(CommandDispatcher<ServerCommandSource> dispatcher) {
		dispatcher.register(literal("outpost")
			.executes(ctx -> usage(ctx.getSource()))
			.then(literal("cost")
				.executes(ctx -> send(ctx.getSource(), "cost", "", false)))
			.then(literal("status")
				.executes(ctx -> send(ctx.getSource(), "status", "", false)))
			.then(literal("cancel")
				.executes(ctx -> send(ctx.getSource(), "cancel", "", true)))
			.then(literal("hold")
				.executes(ctx -> usage(ctx.getSource()))
				.then(argument("length", StringArgumentType.word())
					.executes(ctx -> send(ctx.getSource(), "hold", StringArgumentType.getString(ctx, "length"), true))
					// `confirm` is the second half of a long hold: `/outpost hold 48h`
					// quotes the price, `/outpost hold 48h confirm` does it.
					.then(literal("confirm")
						.executes(ctx -> send(ctx.getSource(), "hold",
							StringArgumentType.getString(ctx, "length") + " confirm", true))))));
	}

	private static int usage(ServerCommandSource source) {
		source.sendFeedback(() -> Text.literal(USAGE), false);
		return 1;
	}

	/**
	 * Asks the daemon, then prints whatever it says.
	 *
	 * <p>The call is asynchronous and the answer is sent back on the server
	 * thread: a tailnet blip must not freeze the tick loop for the length of a
	 * connect timeout, and {@code sendFeedback} is not something to call from
	 * the HTTP client's thread pool.
	 */
	private static int send(ServerCommandSource source, String command, String arg, boolean opOnly) {
		if (opOnly && !source.hasPermissionLevel(OP)) {
			source.sendFeedback(() -> Text.literal("Outpost: only operators can do that."), false);
			return 0;
		}

		MinecraftServer server = source.getServer();
		String player = source.getName();
		CompletableFuture<String> answer = Daemon.call(command, arg, player);

		// A player's client is still there a tick later, so the reply is printed
		// from the server thread and the tick loop never waits on the network.
		if (source.getEntity() != null) {
			answer.whenComplete((reply, failure) -> {
				String message = failure != null ? failed(failure) : reply;
				server.execute(() -> source.sendFeedback(() -> Text.literal(message), false));
			});
			return 1;
		}

		// The console, an RCON client and a command block are all sources with no
		// entity, and none of them read a message that arrives after the command
		// has returned — over RCON the connection is already closed. So those wait.
		// Bounded by the request timeout plus a second, which is the price of a
		// useful answer here.
		String message;
		try {
			message = answer.get(6, TimeUnit.SECONDS);
		} catch (Exception e) {
			message = failed(e);
		}
		String text = message;
		source.sendFeedback(() -> Text.literal(text), false);
		return 1;
	}

	private static String failed(Throwable failure) {
		Throwable cause = failure.getCause() != null ? failure.getCause() : failure;
		return "Outpost: could not reach the daemon (" + cause.getClass().getSimpleName() + ").";
	}
}
