package com.talentserv.blink.service;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.io.ByteArrayOutputStream;
import java.nio.charset.StandardCharsets;

import org.apache.pdfbox.pdmodel.PDDocument;
import org.apache.pdfbox.pdmodel.PDPage;
import org.apache.pdfbox.pdmodel.PDPageContentStream;
import org.apache.pdfbox.pdmodel.font.PDType1Font;
import org.apache.pdfbox.pdmodel.font.Standard14Fonts;
import org.apache.poi.xwpf.usermodel.XWPFDocument;
import org.junit.jupiter.api.Test;
import org.springframework.mock.web.MockMultipartFile;

class RequirementTextExtractorTest {

    private final RequirementTextExtractor extractor = new RequirementTextExtractor();
    private final RequirementMarkdownService markdown = new RequirementMarkdownService(extractor);

    @Test
    void readsPlainTextAndMarkdown() {
        assertThat(extractor.extract("notes.txt", "Add a login screen.\n".getBytes(StandardCharsets.UTF_8)))
                .isEqualTo("Add a login screen.");
        assertThat(extractor.extract("spec.md", "# Portal\n\nShip invoices.".getBytes(StandardCharsets.UTF_8)))
                .contains("Ship invoices.");
    }

    @Test
    void readsDocxParagraphs() throws Exception {
        byte[] bytes;
        try (XWPFDocument document = new XWPFDocument(); ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            document.createParagraph().createRun().setText("Ship a billing portal for invoices.");
            document.write(out);
            bytes = out.toByteArray();
        }
        assertThat(extractor.extract("billing.docx", bytes)).contains("Ship a billing portal for invoices.");
    }

    @Test
    void readsPdfText() throws Exception {
        byte[] bytes;
        try (PDDocument document = new PDDocument()) {
            PDPage page = new PDPage();
            document.addPage(page);
            try (PDPageContentStream stream = new PDPageContentStream(document, page)) {
                stream.beginText();
                stream.setFont(new PDType1Font(Standard14Fonts.FontName.HELVETICA), 12);
                stream.newLineAtOffset(50, 700);
                stream.showText("Build a customer portal");
                stream.endText();
            }
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            document.save(out);
            bytes = out.toByteArray();
        }
        assertThat(extractor.extract("portal.pdf", bytes)).contains("Build a customer portal");
    }

    @Test
    void rejectsPdfWithNoSelectableText() throws Exception {
        byte[] bytes;
        try (PDDocument document = new PDDocument()) {
            document.addPage(new PDPage());
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            document.save(out);
            bytes = out.toByteArray();
        }
        byte[] emptyPdf = bytes;
        assertThatThrownBy(() -> extractor.extract("scan.pdf", emptyPdf))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessageContaining("no selectable text");
    }

    @Test
    void markdownPrefersPastedTextAndFallsBackToExtractedFile() {
        MockMultipartFile file = new MockMultipartFile(
                "file",
                "notes.txt",
                "text/plain",
                "Add login.\n".getBytes(StandardCharsets.UTF_8));
        assertThat(markdown.toMarkdown("Portal", file, "Pasted wins")).contains("Pasted wins").doesNotContain("Add login");
        assertThat(markdown.toMarkdown("Portal", file, "  ")).contains("Add login").doesNotContain("could not extract");
    }
}
