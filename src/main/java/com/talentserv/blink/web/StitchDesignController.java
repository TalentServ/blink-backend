package com.talentserv.blink.web;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;

import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;
import com.talentserv.blink.config.BlinkProperties;
import com.talentserv.blink.error.ApiException;

@RestController
@RequestMapping("/api/integrations/stitch")
public class StitchDesignController {

    private static final JsonMapper MAPPER = JsonMapper.builder().build();
    private static final String MCP = "https://stitch.googleapis.com/mcp";
    private static final String[] NAMES = {"Focused", "Sidebar", "Cards"};
    private static final String[] LAYOUTS = {"linear", "split", "hub"};
    private static final String[] HINTS = {
            "Use a single centered column. One primary action.",
            "Use a left sidebar for navigation and a wide main panel.",
            "Use a card grid. Put the main task in the first card."
    };

    private final BlinkProperties properties;
    private final HttpClient http = HttpClient.newBuilder()
            .followRedirects(HttpClient.Redirect.NORMAL)
            .connectTimeout(Duration.ofSeconds(20))
            .build();

    public StitchDesignController(BlinkProperties properties) {
        this.properties = properties;
    }

    @GetMapping("/media/{id}")
    public ResponseEntity<byte[]> media(@PathVariable String id) throws Exception {
        if (id == null || !id.matches("[a-f0-9\\-]{36}")) {
            throw new ApiException(HttpStatus.NOT_FOUND, "Design image not found.");
        }
        Path file = mediaDir().resolve(id);
        if (!Files.isRegularFile(file)) {
            throw new ApiException(HttpStatus.NOT_FOUND, "Design image not found.");
        }
        byte[] bytes = Files.readAllBytes(file);
        return ResponseEntity.ok().contentType(imageType(bytes)).body(bytes);
    }

    @PostMapping("/designs")
    public Map<String, Object> propose(@RequestBody(required = false) JsonNode body) throws Exception {
        String key = properties.getStitchApiKey() == null ? "" : properties.getStitchApiKey().trim();
        if (key.isBlank()) {
            throw new ApiException(HttpStatus.BAD_REQUEST, "Stitch is not configured. Set STITCH_API_KEY on the server.");
        }
        String prompt = prompt(body);
        if (prompt.isBlank()) {
            throw new ApiException(HttpStatus.BAD_REQUEST, "A groomed requirement is required.");
        }
        String title = text(body, "projectName");
        if (title.isBlank()) {
            title = "Blink design";
        }
        JsonNode created = call(key, "create_project", Map.of("title", title));
        String projectId = resourceId(created);
        if (projectId.isBlank()) {
            throw new ApiException(HttpStatus.BAD_GATEWAY, "Stitch did not return a project.");
        }
        List<CompletableFuture<Map<String, Object>>> jobs = new ArrayList<>();
        for (int i = 0; i < NAMES.length; i++) {
            int index = i;
            String screenPrompt = prompt + "\n\nLayout: " + HINTS[index];
            jobs.add(CompletableFuture.supplyAsync(() -> generate(key, projectId, index, screenPrompt)));
        }
        List<Map<String, Object>> options = new ArrayList<>();
        RuntimeException first = null;
        for (CompletableFuture<Map<String, Object>> job : jobs) {
            try {
                Map<String, Object> option = job.join();
                if (option != null) {
                    options.add(option);
                }
            } catch (Exception ex) {
                if (first == null) {
                    first = new ApiException(HttpStatus.BAD_GATEWAY, ex.getCause() == null ? ex.getMessage() : ex.getCause().getMessage());
                }
            }
        }
        if (options.isEmpty()) {
            throw first == null ? new ApiException(HttpStatus.BAD_GATEWAY, "Stitch did not return a design.") : first;
        }
        Map<String, Object> response = new LinkedHashMap<>();
        response.put("projectId", projectId);
        response.put("message", "Pick one design. Export that screen from Stitch to Figma, then bind the file here.");
        response.put("options", options);
        return response;
    }

    private Map<String, Object> generate(String key, String projectId, int index, String prompt) {
        try {
            JsonNode raw = call(key, "generate_screen_from_text", Map.of(
                    "projectId", projectId,
                    "prompt", prompt,
                    "deviceType", "DESKTOP"
            ));
            String image = storeImage(imageUrl(raw));
            if (image.isBlank()) {
                return null;
            }
            Map<String, Object> option = new LinkedHashMap<>();
            option.put("id", "stitch-" + (index + 1));
            option.put("name", NAMES[index]);
            option.put("summary", HINTS[index]);
            option.put("layout", LAYOUTS[index]);
            option.put("imageUrl", image);
            option.put("screens", List.of());
            return option;
        } catch (Exception ex) {
            throw new ApiException(HttpStatus.BAD_GATEWAY, ex.getMessage());
        }
    }

    private JsonNode call(String key, String tool, Map<String, Object> args) throws Exception {
        String session = openSession(key);
        ObjectNode rpc = MAPPER.createObjectNode();
        rpc.put("jsonrpc", "2.0");
        rpc.put("id", System.currentTimeMillis());
        rpc.put("method", "tools/call");
        ObjectNode params = rpc.putObject("params");
        params.put("name", tool);
        params.set("arguments", MAPPER.valueToTree(args));
        HttpRequest.Builder builder = HttpRequest.newBuilder(URI.create(MCP))
                .timeout(Duration.ofMinutes(4))
                .header("Content-Type", "application/json")
                .header("Accept", "application/json, text/event-stream")
                .header("MCP-Protocol-Version", "2024-11-05")
                .header("X-Goog-Api-Key", key);
        if (!session.isBlank()) {
            builder.header("Mcp-Session-Id", session);
        }
        HttpRequest request = builder.POST(HttpRequest.BodyPublishers.ofString(MAPPER.writeValueAsString(rpc))).build();
        HttpResponse<String> response = http.send(request, HttpResponse.BodyHandlers.ofString());
        if (response.statusCode() < 200 || response.statusCode() >= 300) {
            throw new ApiException(HttpStatus.BAD_GATEWAY, stitchFailure(response.body(), response.statusCode()));
        }
        String payload = rpcPayload(response.body());
        JsonNode envelope = MAPPER.readTree(payload);
        if (envelope.hasNonNull("error")) {
            throw new ApiException(HttpStatus.BAD_GATEWAY, envelope.path("error").path("message").asText("Stitch could not generate this design."));
        }
        JsonNode result = envelope.path("result");
        if (result.path("isError").asBoolean(false)) {
            throw new ApiException(HttpStatus.BAD_GATEWAY, result.path("content").path(0).path("text").asText("Stitch could not generate this design."));
        }
        if (result.hasNonNull("structuredContent")) {
            return result.path("structuredContent");
        }
        String text = result.path("content").path(0).path("text").asText("");
        if (text.startsWith("{") || text.startsWith("[")) {
            return MAPPER.readTree(text);
        }
        return result;
    }

    private String openSession(String key) throws Exception {
        ObjectNode rpc = MAPPER.createObjectNode();
        rpc.put("jsonrpc", "2.0");
        rpc.put("id", 1);
        rpc.put("method", "initialize");
        ObjectNode params = rpc.putObject("params");
        params.put("protocolVersion", "2024-11-05");
        params.putObject("capabilities");
        ObjectNode client = params.putObject("clientInfo");
        client.put("name", "blink");
        client.put("version", "0.1.0");
        HttpRequest request = HttpRequest.newBuilder(URI.create(MCP))
                .timeout(Duration.ofSeconds(30))
                .header("Content-Type", "application/json")
                .header("Accept", "application/json, text/event-stream")
                .header("X-Goog-Api-Key", key)
                .POST(HttpRequest.BodyPublishers.ofString(MAPPER.writeValueAsString(rpc)))
                .build();
        HttpResponse<String> response = http.send(request, HttpResponse.BodyHandlers.ofString());
        if (response.statusCode() < 200 || response.statusCode() >= 300) {
            throw new ApiException(HttpStatus.BAD_GATEWAY, stitchFailure(response.body(), response.statusCode()));
        }
        String session = response.headers().firstValue("mcp-session-id").orElse("");
        if (!session.isBlank()) {
            HttpRequest ready = HttpRequest.newBuilder(URI.create(MCP))
                    .timeout(Duration.ofSeconds(20))
                    .header("Content-Type", "application/json")
                    .header("Accept", "application/json, text/event-stream")
                    .header("MCP-Protocol-Version", "2024-11-05")
                    .header("Mcp-Session-Id", session)
                    .header("X-Goog-Api-Key", key)
                    .POST(HttpRequest.BodyPublishers.ofString("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}"))
                    .build();
            http.send(ready, HttpResponse.BodyHandlers.discarding());
        }
        return session;
    }

    private static String stitchFailure(String body, int status) {
        try {
            JsonNode node = MAPPER.readTree(rpcPayload(body));
            String message = node.path("error").path("message").asText("");
            if (!message.isBlank()) {
                return message;
            }
        } catch (Exception ignored) {
        }
        return "Stitch returned HTTP " + status + ".";
    }

    private static String prompt(JsonNode body) {
        String text = text(body, "requirementText");
        if (body != null && body.path("stories").isArray()) {
            StringBuilder stories = new StringBuilder();
            body.path("stories").forEach(story -> {
                String title = story.path("title").asText(story.path("name").asText(""));
                if (!title.isBlank()) {
                    stories.append("\n- ").append(title);
                }
            });
            if (!stories.isEmpty()) {
                text = text + "\n\nStories:" + stories;
            }
        }
        return text.length() > 4000 ? text.substring(0, 4000) : text;
    }

    private static String text(JsonNode body, String field) {
        return body == null ? "" : body.path(field).asText("").trim();
    }

    private static String resourceId(JsonNode node) {
        if (node == null || node.isNull()) {
            return "";
        }
        if (node.isObject()) {
            for (String field : new String[] {"name", "projectId", "id"}) {
                String value = bareId(node.path(field).asText(""));
                if (!value.isBlank()) {
                    return value;
                }
            }
        }
        if (node.isArray() || node.isObject()) {
            for (JsonNode child : node) {
                String found = resourceId(child);
                if (!found.isBlank()) {
                    return found;
                }
            }
        }
        return "";
    }

    private static String bareId(String value) {
        String trimmed = value == null ? "" : value.trim();
        int slash = trimmed.lastIndexOf('/');
        if (slash >= 0 && slash < trimmed.length() - 1) {
            trimmed = trimmed.substring(slash + 1);
        }
        return trimmed.matches("\\d+") ? trimmed : "";
    }

    private String storeImage(String source) {
        if (source == null || !source.startsWith("http")) {
            return "";
        }
        try {
            HttpRequest request = HttpRequest.newBuilder(URI.create(source))
                    .timeout(Duration.ofSeconds(40))
                    .header("Accept", "image/png,image/jpeg,image/webp,image/*")
                    .GET()
                    .build();
            HttpResponse<byte[]> response = http.send(request, HttpResponse.BodyHandlers.ofByteArray());
            byte[] bytes = response.body();
            if (response.statusCode() < 200 || response.statusCode() >= 300 || bytes == null || bytes.length < 32) {
                return "";
            }
            String id = UUID.randomUUID().toString();
            Files.createDirectories(mediaDir());
            Files.write(mediaDir().resolve(id), bytes);
            return "/api/integrations/stitch/media/" + id;
        } catch (Exception ex) {
            return "";
        }
    }

    private static Path mediaDir() {
        return Path.of(System.getProperty("user.dir", ".")).resolve(".blink-stitch-media");
    }

    private static MediaType imageType(byte[] bytes) {
        if (bytes.length > 2 && (bytes[0] & 0xFF) == 0xFF && (bytes[1] & 0xFF) == 0xD8) {
            return MediaType.IMAGE_JPEG;
        }
        if (bytes.length > 12 && bytes[0] == 'R' && bytes[1] == 'I' && bytes[2] == 'F' && bytes[3] == 'F') {
            return MediaType.parseMediaType("image/webp");
        }
        return MediaType.IMAGE_PNG;
    }

    private static String imageUrl(JsonNode node) {
        if (node == null) {
            return "";
        }
        if (node.isObject()) {
            String shot = node.path("screenshot").path("downloadUrl").asText("");
            if (shot.startsWith("http")) {
                return shot;
            }
            for (JsonNode child : node) {
                String found = imageUrl(child);
                if (!found.isBlank()) {
                    return found;
                }
            }
        }
        if (node.isArray()) {
            for (JsonNode child : node) {
                String found = imageUrl(child);
                if (!found.isBlank()) {
                    return found;
                }
            }
        }
        return "";
    }

    private static String rpcPayload(String raw) {
        String text = raw == null ? "" : raw.trim();
        if (text.startsWith("{")) {
            return text;
        }
        String last = "";
        for (String line : text.split("\n")) {
            String trimmed = line.trim();
            if (trimmed.startsWith("data:")) {
                last = trimmed.substring(5).trim();
            }
        }
        return last.isBlank() ? text : last;
    }
}
