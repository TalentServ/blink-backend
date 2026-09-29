package com.talentserv.blink.service;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.web.multipart.MultipartFile;

@Service
public class RequirementMarkdownService {

    private static final Logger log = LoggerFactory.getLogger(RequirementMarkdownService.class);

    private final RequirementTextExtractor requirementTextExtractor;

    public RequirementMarkdownService(RequirementTextExtractor requirementTextExtractor) {
        this.requirementTextExtractor = requirementTextExtractor;
    }

    public String toMarkdown(String projectName, MultipartFile file, String pastedText) {
        String title = projectName == null || projectName.isBlank() ? "Project" : projectName.trim();
        if (pastedText != null && !pastedText.isBlank()) {
            return withTitle(title, pastedText.strip());
        }
        if (file != null && !file.isEmpty()) {
            try {
                String extracted = requirementTextExtractor.extract(file.getOriginalFilename(), file.getBytes());
                return withTitle(title, extracted);
            } catch (RuntimeException ex) {
                log.warn("Could not extract requirement text from {}: {}", file.getOriginalFilename(), ex.getMessage());
                return unreadPlaceholder(title, file.getOriginalFilename());
            } catch (Exception ex) {
                log.warn("Could not extract requirement text from {}: {}", file.getOriginalFilename(), ex.toString());
                return unreadPlaceholder(title, file.getOriginalFilename());
            }
        }
        throw new IllegalArgumentException("Upload a document or paste requirements.");
    }

    private static String unreadPlaceholder(String title, String fileName) {
        String name = fileName == null || fileName.isBlank() ? "upload" : fileName;
        return """
                # %s

                Requirement document uploaded: `%s`

                Blink could not extract text from this file. Replace this page with the full
                requirements before running the SDLC workflow.
                """.formatted(title, name);
    }

    private static String withTitle(String title, String body) {
        if (body.startsWith("#")) {
            return body;
        }
        return "# " + title + "\n\n" + body + "\n";
    }
}
