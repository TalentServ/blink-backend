package com.talentserv.blink.web;

import java.io.IOException;

import org.springframework.http.MediaType;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.multipart.MultipartFile;

import com.talentserv.blink.service.RequirementTextExtractor;

@RestController
@RequestMapping("/api/requirements")
public class RequirementExtractController {

    private final RequirementTextExtractor requirementTextExtractor;

    public RequirementExtractController(RequirementTextExtractor requirementTextExtractor) {
        this.requirementTextExtractor = requirementTextExtractor;
    }

    @PostMapping(path = "/extract", consumes = MediaType.MULTIPART_FORM_DATA_VALUE)
    public RequirementExtractResponse extract(@RequestParam("file") MultipartFile file) throws IOException {
        if (file == null || file.isEmpty()) {
            throw new IllegalArgumentException("Upload a document.");
        }
        String text = requirementTextExtractor.extract(file.getOriginalFilename(), file.getBytes());
        String fileName = file.getOriginalFilename() == null ? "upload" : file.getOriginalFilename();
        return new RequirementExtractResponse(fileName, text);
    }

    public record RequirementExtractResponse(String fileName, String text) {
    }
}
